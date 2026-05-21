package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/runaway-killswitch/proxy-engine/core"
)

func TestComputePromptHash(t *testing.T) {
	msgBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	msgBody2 := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`)
	diffBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"goodbye"}]}`)
	emptyMsg := []byte(`{"model":"gpt-4o","messages":[]}`)
	noMsg := []byte(`{"model":"gpt-4o"}`)

	hash1 := computePromptHash(msgBody)
	hash2 := computePromptHash(msgBody2)
	hash3 := computePromptHash(diffBody)

	// Same messages, different extra fields → same hash
	if hash1 != hash2 {
		t.Error("same messages with different extra fields should produce same hash")
	}

	// Different messages → different hash
	if hash1 == hash3 {
		t.Error("different messages should produce different hashes")
	}

	// Empty messages array → falls back to full body hash
	emptyHash := computePromptHash(emptyMsg)
	if emptyHash == hash1 {
		t.Error("empty messages should not match non-empty messages hash")
	}

	// No messages field → falls back to full body hash
	noMsgHash := computePromptHash(noMsg)
	if noMsgHash == hash1 {
		t.Error("missing messages should not match non-empty messages hash")
	}

	// Malformed JSON → falls back to full body hash
	malformedHash := computePromptHash([]byte(`{bad json`))
	if malformedHash == "" {
		t.Error("malformed JSON should still produce a hash")
	}

	// Empty body → still produces a hash
	emptyHash2 := computePromptHash([]byte{})
	if emptyHash2 == "" {
		t.Error("empty body should still produce a hash")
	}
}

func TestExtractModel(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "present model field",
			body: `{"model":"gpt-4o","messages":[]}`,
			want: "gpt-4o",
		},
		{
			name: "missing model field",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
			want: "unknown",
		},
		{
			name: "empty model string",
			body: `{"model":"","messages":[]}`,
			want: "unknown",
		},
		{
			name: "malformed JSON",
			body: `{bad`,
			want: "unknown",
		},
		{
			name: "empty body",
			body: `{}`,
			want: "unknown",
		},
		{
			name: "anthropic model string",
			body: `{"model":"claude-sonnet-4-6-20251001","max_tokens":100}`,
			want: "claude-sonnet-4-6-20251001",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractModel([]byte(tt.body))
			if got != tt.want {
				t.Errorf("extractModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInjectStreamOptions(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantExact      string   // non-empty: assert exact byte equality
		wantStreamKeys []string // non-nil: assert these keys exist in stream_options
	}{
		{
			name:           "streaming request injects stream_options",
			body:           `{"model":"gpt-4o","stream":true,"messages":[]}`,
			wantStreamKeys: []string{"include_usage"},
		},
		{
			name:      "non-streaming request unchanged",
			body:      `{"model":"gpt-4o","stream":false,"messages":[]}`,
			wantExact: `{"model":"gpt-4o","stream":false,"messages":[]}`,
		},
		{
			name:      "no stream field unchanged",
			body:      `{"model":"gpt-4o","messages":[]}`,
			wantExact: `{"model":"gpt-4o","messages":[]}`,
		},
		{
			name:      "malformed body unchanged",
			body:      `{bad json`,
			wantExact: `{bad json`,
		},
		{
			name:           "existing stream_options merged, preserves other fields",
			body:           `{"model":"gpt-4o","stream":true,"stream_options":{"include_content_filter":true}}`,
			wantStreamKeys: []string{"include_content_filter", "include_usage"},
		},
		{
			name:           "existing stream_options with include_usage already set",
			body:           `{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true}}`,
			wantStreamKeys: []string{"include_usage"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectStreamOptions([]byte(tt.body))
			if tt.wantExact != "" {
				if string(got) != tt.wantExact {
					t.Errorf("injectStreamOptions() = %s, want %s", string(got), tt.wantExact)
				}
				return
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(got, &result); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			so, ok := result["stream_options"]
			if !ok {
				t.Fatal("result missing stream_options")
			}
			var opts map[string]json.RawMessage
			if err := json.Unmarshal(so, &opts); err != nil {
				t.Fatalf("stream_options is not valid JSON: %v", err)
			}
			for _, key := range tt.wantStreamKeys {
				if _, has := opts[key]; !has {
					t.Errorf("stream_options missing %q", key)
				}
			}
		})
	}
}

func TestResolveOpenAITarget(t *testing.T) {
	cfg := &core.Config{
		Routing: core.RoutingConfig{
			DefaultOpenAIProvider: "openai",
			Providers: map[string]core.ProviderConfig{
				"openai":     {BaseURL: "https://api.openai.com"},
				"deepseek":   {BaseURL: "https://api.deepseek.com"},
				"openrouter": {BaseURL: "https://openrouter.ai/api"},
			},
		},
	}
	srv := &ProxyServer{config: cfg}

	tests := []struct {
		name         string
		model        string
		wantBaseURL  string
		wantProvider string
	}{
		{
			name:         "deepseek prefix",
			model:        "deepseek-chat",
			wantBaseURL:  "https://api.deepseek.com",
			wantProvider: "deepseek",
		},
		{
			name:         "deepseek prefix case insensitive",
			model:        "DeepSeek-Coder",
			wantBaseURL:  "https://api.deepseek.com",
			wantProvider: "deepseek",
		},
		{
			name:         "gpt-4o prefix",
			model:        "gpt-4o",
			wantBaseURL:  "https://api.openai.com",
			wantProvider: "openai",
		},
		{
			name:         "gpt-4o-mini prefix",
			model:        "gpt-4o-mini-2024-07-18",
			wantBaseURL:  "https://api.openai.com",
			wantProvider: "openai",
		},
		{
			name:         "o1 prefix",
			model:        "o1-preview",
			wantBaseURL:  "https://api.openai.com",
			wantProvider: "openai",
		},
		{
			name:         "o3 prefix",
			model:        "o3-mini",
			wantBaseURL:  "https://api.openai.com",
			wantProvider: "openai",
		},
		{
			name:         "chatgpt prefix",
			model:        "chatgpt-4o-latest",
			wantBaseURL:  "https://api.openai.com",
			wantProvider: "openai",
		},
		{
			name:         "unknown model falls back to default provider",
			model:        "some-unknown-model",
			wantBaseURL:  "https://api.openai.com",
			wantProvider: "openai",
		},
		{
			name:         "openrouter prefix",
			model:        "openrouter/auto",
			wantBaseURL:  "https://openrouter.ai/api",
			wantProvider: "openrouter",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL, provider := srv.resolveOpenAITarget(tt.model)
			if baseURL != tt.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", baseURL, tt.wantBaseURL)
			}
			if provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", provider, tt.wantProvider)
			}
		})
	}
}

func TestResolveOpenAITargetDeterministic(t *testing.T) {
	// Verify that overlapping provider keys match longest prefix first.
	cfg := &core.Config{
		Routing: core.RoutingConfig{
			DefaultOpenAIProvider: "openai",
			Providers: map[string]core.ProviderConfig{
				"open": {BaseURL: "https://api.open.example.com"},
				"openai": {BaseURL: "https://api.openai.com"},
			},
		},
	}
	srv := &ProxyServer{config: cfg}

	// Run multiple times to ensure non-randomness
	for i := 0; i < 10; i++ {
		baseURL, provider := srv.resolveOpenAITarget("openai-gpt-4o")
		if baseURL != "https://api.openai.com" {
			t.Errorf("iteration %d: baseURL = %q, want %q (longest prefix should win)", i, baseURL, "https://api.openai.com")
		}
		if provider != "openai" {
			t.Errorf("iteration %d: provider = %q, want %q", i, provider, "openai")
		}
	}
}

func TestBuildUpstreamURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		path     string
		rawQuery string
		want     string
	}{
		{
			name:     "no query string",
			baseURL:  "https://api.openai.com",
			path:     "/v1/chat/completions",
			rawQuery: "",
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "with query string",
			baseURL:  "https://api.openai.com",
			path:     "/v1/chat/completions",
			rawQuery: "api-version=2024-01-01",
			want:     "https://api.openai.com/v1/chat/completions?api-version=2024-01-01",
		},
		{
			name:     "anthropic messages endpoint",
			baseURL:  "https://api.anthropic.com",
			path:     "/v1/messages",
			rawQuery: "",
			want:     "https://api.anthropic.com/v1/messages",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildUpstreamURL(tt.baseURL, tt.path, tt.rawQuery)
			if got != tt.want {
				t.Errorf("buildUpstreamURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteBlockedResponse(t *testing.T) {
	rec := newResponseRecorder()
	writeBlockedResponse(rec, "test reason")

	if rec.statusCode != 402 {
		t.Errorf("status code = %d, want 402", rec.statusCode)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rec.body.Bytes(), &result); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	errObj, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing error object")
	}
	if errObj["type"] != "circuit_breaker_active" {
		t.Errorf("error.type = %v, want circuit_breaker_active", errObj["type"])
	}
	if errObj["message"] != "RunawayKillSwitch: test reason" {
		t.Errorf("error.message = %v, want RunawayKillSwitch: test reason", errObj["message"])
	}
	if errObj["proxy"] != "runaway-killswitch" {
		t.Errorf("error.proxy = %v, want runaway-killswitch", errObj["proxy"])
	}
}

// responseRecorder is a minimal http.ResponseWriter for testing.
// Mirrors the implicit-200 behaviour of net/http: the first call to Write
// that precedes any WriteHeader call triggers an implicit WriteHeader(200).
type responseRecorder struct {
	statusCode  int
	header      http.Header
	body        *bytes.Buffer
	wroteHeader bool
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{body: &bytes.Buffer{}}
}

func (r *responseRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.statusCode = code
		r.wroteHeader = true
	}
}
