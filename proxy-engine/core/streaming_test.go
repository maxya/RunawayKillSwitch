package core

import "testing"

func TestParseSSEEventTokens(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		provider   string
		wantInput  int64
		wantOutput int64
	}{
		{
			name:       "anthropic message_start",
			provider:   "anthropic",
			data:       `{"type":"message_start","message":{"usage":{"input_tokens":42}}}`,
			wantInput:  42,
			wantOutput: 0,
		},
		{
			name:       "anthropic message_delta",
			provider:   "anthropic",
			data:       `{"type":"message_delta","usage":{"output_tokens":17}}`,
			wantInput:  0,
			wantOutput: 17,
		},
		{
			name:       "anthropic content_block_start ignored",
			provider:   "anthropic",
			data:       `{"type":"content_block_start","index":0}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "anthropic ping event ignored",
			provider:   "anthropic",
			data:       `{"type":"ping"}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "openai final chunk with usage",
			provider:   "openai",
			data:       `{"usage":{"prompt_tokens":100,"completion_tokens":50}}`,
			wantInput:  100,
			wantOutput: 50,
		},
		{
			name:       "openai partial chunk no usage",
			provider:   "openai",
			data:       `{"choices":[{"delta":{"content":"hello"}}]}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "openai content chunk",
			provider:   "openai",
			data:       `{"id":"chatcmpl-123","choices":[{"delta":{"content":"world"},"index":0}]}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "deepseek same format as openai",
			provider:   "deepseek",
			data:       `{"usage":{"prompt_tokens":200,"completion_tokens":80}}`,
			wantInput:  200,
			wantOutput: 80,
		},
		{
			name:       "openrouter same format as openai",
			provider:   "openrouter",
			data:       `{"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			wantInput:  5,
			wantOutput: 3,
		},
		{
			name:       "unknown provider defaults to openai format",
			provider:   "unknown",
			data:       `{"usage":{"prompt_tokens":10,"completion_tokens":20}}`,
			wantInput:  10,
			wantOutput: 20,
		},
		{
			name:       "malformed JSON returns zeros",
			provider:   "anthropic",
			data:       `{not valid json}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "empty object returns zeros",
			provider:   "anthropic",
			data:       `{}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "anthropic message_start with extra fields",
			provider:   "anthropic",
			data:       `{"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":999},"model":"claude-sonnet-4-6"},"index":0}`,
			wantInput:  999,
			wantOutput: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, out := ParseSSEEventTokens([]byte(tt.data), tt.provider)
			if in != tt.wantInput {
				t.Errorf("inputTokens = %d, want %d", in, tt.wantInput)
			}
			if out != tt.wantOutput {
				t.Errorf("outputTokens = %d, want %d", out, tt.wantOutput)
			}
		})
	}
}

func TestParseNonStreamingTokens(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		provider   string
		wantInput  int64
		wantOutput int64
	}{
		{
			name:       "anthropic format",
			provider:   "anthropic",
			body:       `{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":5}}`,
			wantInput:  10,
			wantOutput: 5,
		},
		{
			name:       "anthropic format with zero tokens",
			provider:   "anthropic",
			body:       `{"id":"msg_123","type":"message","usage":{"input_tokens":0,"output_tokens":0}}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "openai format",
			provider:   "openai",
			body:       `{"id":"chatcmpl-123","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantInput:  10,
			wantOutput: 5,
		},
		{
			name:       "deepseek format",
			provider:   "deepseek",
			body:       `{"id":"ds-123","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50}}`,
			wantInput:  100,
			wantOutput: 50,
		},
		{
			name:       "empty body",
			provider:   "anthropic",
			body:       `{}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "malformed JSON",
			provider:   "openai",
			body:       `{bad json`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "anthropic missing usage field",
			provider:   "anthropic",
			body:       `{"id":"msg_123","type":"message","content":[]}`,
			wantInput:  0,
			wantOutput: 0,
		},
		{
			name:       "openai missing usage field",
			provider:   "openai",
			body:       `{"id":"chatcmpl-123","choices":[]}`,
			wantInput:  0,
			wantOutput: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, out := ParseNonStreamingTokens([]byte(tt.body), tt.provider)
			if in != tt.wantInput {
				t.Errorf("inputTokens = %d, want %d", in, tt.wantInput)
			}
			if out != tt.wantOutput {
				t.Errorf("outputTokens = %d, want %d", out, tt.wantOutput)
			}
		})
	}
}
