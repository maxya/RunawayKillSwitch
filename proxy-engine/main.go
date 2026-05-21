package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/runaway-killswitch/proxy-engine/core"
)

//go:embed ui/embedded_dashboard.html
var dashboardHTML []byte

// webhookClient is used exclusively for circuit breaker notifications.
var webhookClient = &http.Client{Timeout: 10 * time.Second}

// httpClient is shared across all proxy requests with a generous timeout for LLM responses.
var httpClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// ProxyServer holds the application state for HTTP handlers.
type ProxyServer struct {
	config  *core.Config
	metrics *core.RedisMetricsStore
	breaker *core.CircuitBreaker
}

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "/app/config/killswitch.yaml"
	}

	cfg, err := core.LoadConfig(configPath)
	if err != nil {
		slog.Error("failed to load config", "path", configPath, "error", err)
		os.Exit(1)
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://state-db:6379/0"
	}

	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		slog.Error("failed to connect to Redis", "url", redisURL, "error", err)
		os.Exit(1)
	}

	srv := &ProxyServer{
		config:  cfg,
		metrics: store,
		breaker: core.NewCircuitBreaker(store, cfg),
	}

	// Proxy mux — receives AI API traffic from agents
	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("/v1/messages", srv.handleAnthropic)
	proxyMux.HandleFunc("/v1/chat/completions", srv.handleOpenAICompat)

	// Admin mux — dashboard and management API
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/", srv.serveUI)
	adminMux.HandleFunc("/api/status", srv.handleAPIStatus)
	adminMux.HandleFunc("/api/reset", srv.handleAPIReset)

	adminServer := &http.Server{
		Addr:              ":8531",
		Handler:           adminMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	proxyServer := &http.Server{
		Addr:    ":8530",
		Handler: proxyMux,
		// ReadHeaderTimeout guards against Slowloris; no ReadTimeout/WriteTimeout because
		// LLM responses can stream for minutes.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("admin UI listening", "addr", adminServer.Addr)
		if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("admin server crashed", "error", err)
			os.Exit(1)
		}
	}()

	go func() {
		slog.Info("proxy listening", "addr", proxyServer.Addr)
		if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("proxy engine crashed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutdown signal received, draining connections")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := proxyServer.Shutdown(shutCtx); err != nil {
		slog.Error("proxy server shutdown error", "error", err)
	}
	if err := adminServer.Shutdown(shutCtx); err != nil {
		slog.Error("admin server shutdown error", "error", err)
	}
	slog.Info("shutdown complete")
}

// handleAnthropic handles POST /v1/messages and routes to api.anthropic.com.
func (s *ProxyServer) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB limit
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body.Close()

	promptHash := computePromptHash(bodyBytes)
	model := extractModel(bodyBytes)
	ctx := r.Context()

	blocked, reason, err := s.breaker.PreRequestCheck(ctx, promptHash)
	if err != nil {
		slog.Error("pre-request check failed, failing open", "error", err)
	}
	if blocked {
		writeBlockedResponse(w, reason)
		return
	}

	targetURL := buildUpstreamURL("https://api.anthropic.com", r.URL.Path, r.URL.RawQuery)
	inputTokens, outputTokens := s.forwardRequest(w, r, bodyBytes, targetURL, "anthropic")

	if triggered, triggerReason, err := s.breaker.PostResponseRecord(ctx, model, inputTokens, outputTokens); err != nil {
		slog.Error("post-response record error", "error", err)
	} else if triggered {
		slog.Info("circuit breaker triggered", "reason", triggerReason)
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("webhook goroutine panicked", "recover", rec)
				}
			}()
			s.fireWebhook(triggerReason)
		}()
	}
}

// handleOpenAICompat handles POST /v1/chat/completions and routes to openai/deepseek/openrouter
// based on the model name in the request body.
func (s *ProxyServer) handleOpenAICompat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB limit
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body.Close()

	model := extractModel(bodyBytes)
	promptHash := computePromptHash(bodyBytes)
	targetBaseURL, provider := s.resolveOpenAITarget(model)

	// Inject stream_options so OpenAI returns token counts in the final SSE chunk.
	bodyBytes = injectStreamOptions(bodyBytes)
	ctx := r.Context()

	blocked, reason, err := s.breaker.PreRequestCheck(ctx, promptHash)
	if err != nil {
		slog.Error("pre-request check failed, failing open", "error", err)
	}
	if blocked {
		writeBlockedResponse(w, reason)
		return
	}

	targetURL := buildUpstreamURL(targetBaseURL, r.URL.Path, r.URL.RawQuery)
	inputTokens, outputTokens := s.forwardRequest(w, r, bodyBytes, targetURL, provider)

	if triggered, triggerReason, err := s.breaker.PostResponseRecord(ctx, model, inputTokens, outputTokens); err != nil {
		slog.Error("post-response record error", "error", err)
	} else if triggered {
		slog.Info("circuit breaker triggered", "reason", triggerReason)
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("webhook goroutine panicked", "recover", rec)
				}
			}()
			s.fireWebhook(triggerReason)
		}()
	}
}

// forwardRequest sends bodyBytes to targetURL, streams/buffers the response back to w,
// and returns the token counts extracted from the response.
func (s *ProxyServer) forwardRequest(w http.ResponseWriter, r *http.Request, bodyBytes []byte, targetURL string, provider string) (inputTokens, outputTokens int64) {
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to build upstream request", http.StatusInternalServerError)
		return
	}

	// Copy all client headers (preserves API keys, anthropic-version, content-type, etc.)
	for k, vs := range r.Header {
		if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
			continue
		}
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	outReq.ContentLength = int64(len(bodyBytes))

	resp, err := httpClient.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Upstream error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	hopByHop := map[string]bool{
		"Transfer-Encoding": true,
		"Trailer":           true,
		"Keep-Alive":        true,
		"Proxy-Connection":  true,
		"Upgrade":           true,
		"Connection":        true,
	}
	for k, vs := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.Warn("error copying upstream error response", "error", err)
		}
		return
	}

	isSSE := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if isSSE {
		inputTokens, outputTokens = streamAndCapture(w, resp.Body, provider)
	} else {
		fullBody, _ := io.ReadAll(resp.Body)
		inputTokens, outputTokens = core.ParseNonStreamingTokens(fullBody, provider)
		if _, err := w.Write(fullBody); err != nil {
			slog.Warn("error writing response body to client", "error", err)
		}
	}
	return
}

// streamAndCapture reads the SSE response line by line, forwards each line to w immediately
// (preserving real-time streaming to the client), and extracts token counts from data events.
func streamAndCapture(w http.ResponseWriter, body io.Reader, provider string) (inputTokens, outputTokens int64) {
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 4096)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				slog.Warn("error writing SSE line to client", "error", werr)
				break
			}
			if canFlush {
				flusher.Flush()
			}
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data: ")) {
				data := trimmed[6:]
				if !bytes.Equal(data, []byte("[DONE]")) {
					in, out := core.ParseSSEEventTokens(data, provider)
					if in > 0 {
						inputTokens = in
					}
					if out > 0 {
						outputTokens = out
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return
}

// resolveOpenAITarget maps a model name to the correct upstream base URL and provider label.
// Priority: config routing table → built-in prefix rules → config default → openai fallback.
func (s *ProxyServer) resolveOpenAITarget(model string) (baseURL string, provider string) {
	lower := strings.ToLower(model)

	// Sort longest prefix first for deterministic matching; prevents a short key eating a longer match.
	providerKeys := make([]string, 0, len(s.config.Routing.Providers))
	for k := range s.config.Routing.Providers {
		providerKeys = append(providerKeys, k)
	}
	sort.Slice(providerKeys, func(i, j int) bool {
		return len(providerKeys[i]) > len(providerKeys[j])
	})
	for _, prefix := range providerKeys {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return s.config.Routing.Providers[prefix].BaseURL, prefix
		}
	}

	if strings.HasPrefix(lower, "deepseek") {
		return "https://api.deepseek.com", "deepseek"
	}
	if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") ||
		strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "chatgpt") {
		return "https://api.openai.com", "openai"
	}

	if dp := s.config.Routing.DefaultOpenAIProvider; dp != "" {
		if prov, ok := s.config.Routing.Providers[dp]; ok {
			return prov.BaseURL, dp
		}
	}
	return "https://api.openai.com", "openai"
}

// Admin HTTP handlers

func (s *ProxyServer) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *ProxyServer) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	summary, err := s.metrics.GetMetricsSummary(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "redis unavailable"})
		return
	}
	json.NewEncoder(w).Encode(summary)
}

func (s *ProxyServer) handleAPIReset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	if err := s.metrics.ResetCircuitBreaker(r.Context()); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "reset failed"})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "reset", "message": "Circuit breaker reset successfully"})
}

func (s *ProxyServer) fireWebhook(reason string) {
	if s.config.Notifications.SystemBell {
		fmt.Print("\a")
	}
	if !s.config.Notifications.Webhook.Enabled || s.config.Notifications.Webhook.URL == "" {
		return
	}
	payload := map[string]interface{}{
		"content": fmt.Sprintf("🚨 **RunawayKillSwitch TRIGGERED**\n**Reason:** %s\n**Time:** %s",
			reason, time.Now().Format(time.RFC3339)),
	}
	data, _ := json.Marshal(payload)
	resp, err := webhookClient.Post(s.config.Notifications.Webhook.URL, "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Error("webhook delivery failed", "error", err)
		return
	}
	resp.Body.Close()
}

// Helper functions

// writeBlockedResponse sends an HTTP 402 with a JSON error body explaining the block reason.
func writeBlockedResponse(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"type":    "circuit_breaker_active",
			"message": "RunawayKillSwitch: " + reason,
			"proxy":   "runaway-killswitch",
		},
	})
}

// computePromptHash hashes the messages array from the request body.
// Hashing only messages (not temperature, max_tokens, etc.) means the hash
// is stable across retries that differ only in sampling parameters.
func computePromptHash(body []byte) string {
	var req struct {
		Messages json.RawMessage `json:"messages"`
	}
	// len > 2 is a byte-length guard: an empty messages array "[]" is exactly 2 bytes.
	// Any non-empty messages array is longer, so this check means "has at least one message".
	if err := json.Unmarshal(body, &req); err == nil && len(req.Messages) > 2 {
		h := sha256.Sum256(req.Messages)
		return hex.EncodeToString(h[:])
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// extractModel reads the model field from a JSON request body.
func extractModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil && req.Model != "" {
		return req.Model
	}
	return "unknown"
}

// buildUpstreamURL constructs the full upstream URL from base, path, and query string.
func buildUpstreamURL(baseURL, path, rawQuery string) string {
	if rawQuery != "" {
		return baseURL + path + "?" + rawQuery
	}
	return baseURL + path
}

// injectStreamOptions modifies an OpenAI-format request body to include
// stream_options: {include_usage: true} when streaming is enabled.
// This causes the final SSE chunk to carry token usage counts.
// Returns the original body unchanged if the request is not streaming.
func injectStreamOptions(bodyBytes []byte) []byte {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return bodyBytes
	}
	streamVal, exists := req["stream"]
	if !exists {
		return bodyBytes
	}
	var isStream bool
	if err := json.Unmarshal(streamVal, &isStream); err != nil || !isStream {
		return bodyBytes
	}
	// Merge include_usage into stream_options without destroying other fields.
	if existing, has := req["stream_options"]; has {
		var opts map[string]json.RawMessage
		if json.Unmarshal(existing, &opts) == nil {
			opts["include_usage"] = json.RawMessage("true")
			if merged, err := json.Marshal(opts); err == nil {
				req["stream_options"] = merged
			}
		}
		// If unmarshal fails, fall through and set the whole object.
	} else {
		req["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	modified, err := json.Marshal(req)
	if err != nil {
		return bodyBytes
	}
	return modified
}
