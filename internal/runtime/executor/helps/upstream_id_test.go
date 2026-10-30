package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageRecordUpstreamIDFamilies(t *testing.T) {
	for _, tc := range []struct{ name, provider, body, start, end, id string }{
		{"claude", "claude", `{"type":"message","id":"msg_1","usage":{"input_tokens":1}}`, `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1}}}`, `{"type":"message_delta","usage":{"output_tokens":2}}`, "msg_1"},
		{"codex", "codex", `{"id":"resp_1","object":"response"}`, `{"type":"response.created","response":{"id":"resp_1"}}`, `{"type":"response.completed","response":{"id":"resp_1"}}`, "resp_1"},
		{"openai-responses", "openai", `{"id":"resp_2","object":"response"}`, `{"type":"response.created","response":{"id":"resp_2"}}`, `{"type":"response.completed","response":{"id":"resp_2"}}`, "resp_2"},
		{"openai-chat", "openai", `{"id":"chatcmpl-1","object":"chat.completion"}`, `{"id":"chatcmpl-1","object":"chat.completion.chunk"}`, `{"choices":[{"finish_reason":"stop"}]}`, "chatcmpl-1"},
		{"gemini", "gemini", `{"responseId":"gemini-1"}`, `{"responseId":"gemini-1"}`, `{"candidates":[{"finishReason":"STOP"}]}`, "gemini-1"},
		{"vertex", "vertex", `{"responseId":"vertex-1"}`, `{"responseId":"vertex-1"}`, `{}`, "vertex-1"},
		{"aistudio", "aistudio", `{"responseId":"studio-1"}`, `{"responseId":"studio-1"}`, `{}`, "studio-1"},
		{"antigravity", "antigravity", `{"response":{"responseId":"ag-1"}}`, `{"response":{"responseId":"ag-1"}}`, `{}`, "ag-1"},
		{"kimi", "kimi", `{"id":"chatcmpl-kimi"}`, `{"id":"chatcmpl-kimi"}`, `{}`, "chatcmpl-kimi"},
		{"xai", "xai", `{"id":"resp-xai"}`, `{"type":"response.created","response":{"id":"resp-xai"}}`, `{}`, "resp-xai"},
		{"meta", "meta", `{"id":"chatcmpl-meta"}`, `{"id":"chatcmpl-meta"}`, `{}`, "chatcmpl-meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				reporter := NewUsageReporter(context.Background(), tc.provider, "model", nil)
				if stream {
					reporter.ObserveResponseModel([]byte("data: " + tc.start))
					if got := reporter.buildRecord(usage.Detail{}, true).UpstreamID; got != tc.id {
						t.Fatalf("early failure ID=%q, want %q", got, tc.id)
					}
					reporter.ObserveResponseModel([]byte("data: " + tc.end))
					reporter.ObserveResponseModel([]byte(`data: {"type":"response.output_item.added","id":"event_1","item":{"id":"item_1"}}`))
				} else {
					reporter.ObserveResponseModel([]byte(tc.body))
				}
				record := reporter.buildRecord(usage.Detail{}, false)
				if record.UpstreamID != tc.id {
					t.Fatalf("stream=%v ID=%q, want %q", stream, record.UpstreamID, tc.id)
				}
				if got := reporter.buildRecordForModel("image-model", usage.Detail{}, false, usage.Failure{}).UpstreamID; got != tc.id {
					t.Fatalf("additional model ID=%q", got)
				}
			}
		})
	}
}

func TestStreamUsageBufferUpstreamID(t *testing.T) {
	for _, claude := range []bool{false, true} {
		var buffer StreamUsageBuffer
		want := "chatcmpl-1"
		if claude {
			want = "msg_1"
			buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1}}}`))
			buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"output_tokens":2}}`))
		} else {
			buffer.ObserveOpenAIStream([]byte(`data: {"id":"chatcmpl-1","choices":[]}`))
			buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
		}
		reporter := NewUsageReporter(context.Background(), "test", "model", nil)
		if !buffer.Publish(context.Background(), reporter) {
			t.Fatal("no usage published")
		}
		if got := reporter.buildRecord(usage.Detail{}, false).UpstreamID; got != want {
			t.Fatalf("ID=%q, want %q", got, want)
		}
	}
}

func TestUpstreamIDAbsentOrInvalid(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":123}`, `{"id":"broken"`, `{"type":"response.output_item.added","id":"event","item":{"id":"item"}}`, "data: [DONE]"} {
		if got := extractUpstreamID([]byte(body)); got != "" {
			t.Fatalf("%s: ID=%q", body, got)
		}
	}
}

func TestStreamUsageBufferUpstreamIDWithoutUsage(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream([]byte(`data: {"id":"chatcmpl-no-usage","choices":[]}`))
	reporter := NewUsageReporter(context.Background(), "openai", "model", nil)
	if buffer.Publish(context.Background(), reporter) {
		t.Fatal("unexpected usage")
	}
	if got := reporter.buildRecord(usage.Detail{}, false).UpstreamID; got != "chatcmpl-no-usage" {
		t.Fatalf("ID=%q", got)
	}
}
