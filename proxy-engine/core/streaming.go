package core

import "encoding/json"

// ParseSSEEventTokens extracts token counts from a single SSE data: line payload.
// Returns (inputTokens, outputTokens); zero means not present in this particular event.
// Called once per SSE line as the stream passes through the proxy.
func ParseSSEEventTokens(data []byte, provider string) (inputTokens, outputTokens int64) {
	if provider == "anthropic" {
		return parseAnthropicSSEEvent(data)
	}
	return parseOpenAISSEEvent(data)
}

// ParseNonStreamingTokens extracts token counts from a buffered non-streaming response body.
func ParseNonStreamingTokens(body []byte, provider string) (inputTokens, outputTokens int64) {
	if provider == "anthropic" {
		var resp struct {
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil {
			return resp.Usage.InputTokens, resp.Usage.OutputTokens
		}
		return 0, 0
	}

	// OpenAI-compatible format (openai, deepseek, openrouter)
	var resp struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err == nil {
		return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	return 0, 0
}

// parseAnthropicSSEEvent handles Anthropic streaming events.
// "message_start" carries input_tokens under message.usage.
// "message_delta" carries the cumulative output_tokens under usage.
func parseAnthropicSSEEvent(data []byte) (inputTokens, outputTokens int64) {
	var evt struct {
		Type    string `json:"type"`
		Message struct {
			Usage struct {
				InputTokens int64 `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		return 0, 0
	}
	switch evt.Type {
	case "message_start":
		return evt.Message.Usage.InputTokens, 0
	case "message_delta":
		return 0, evt.Usage.OutputTokens
	}
	return 0, 0
}

// parseOpenAISSEEvent handles OpenAI-compatible streaming events.
// Token usage only appears in the final chunk when stream_options.include_usage=true
// was set in the request (injected by the proxy automatically).
func parseOpenAISSEEvent(data []byte) (inputTokens, outputTokens int64) {
	var evt struct {
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &evt); err != nil || evt.Usage == nil {
		return 0, 0
	}
	return evt.Usage.PromptTokens, evt.Usage.CompletionTokens
}
