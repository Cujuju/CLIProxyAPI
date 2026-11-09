package helps

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/sjson"
)

func TestRequestSettingsTranslatedClaude(t *testing.T) {
	body, err := responsesclaude.ConvertOpenAIResponsesRequestToClaude("claude-sonnet-5", []byte(`{"model":"gpt-5","input":"hello","max_output_tokens":2048}`), false)
	if err != nil {
		t.Fatal(err)
	}
	body, err = sjson.SetBytes(body, "thinking", map[string]any{"type": "enabled", "budget_tokens": 1024})
	if err != nil {
		t.Fatal(err)
	}
	reporter := NewUsageReporter(context.Background(), "claude", "claude-sonnet-5", nil)
	reporter.SetTranslatedRequestMetadata(body, "claude")
	reporter.SetRequestBetas(http.Header{"Anthropic-Beta": {"z-beta, a-beta", "a-beta"}})
	settings := reporter.buildRecord(usage.Detail{}, false).RequestSettings
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"thinking":"enabled","thinking_budget":1024,"max_tokens":2048,"betas":["a-beta","z-beta"]}` {
		t.Fatalf("settings=%s; translated=%s", data, body)
	}
}

func TestRequestSettingsUnknownAndOpenAI(t *testing.T) {
	for _, tc := range []struct{ format, body, want string }{
		{"claude", `{}`, `null`},
		{"claude", `{"thinking":{"type":"adaptive"},"max_tokens":0}`, `{"thinking":"adaptive","max_tokens":0}`},
		{"openai", `{"reasoning_effort":"none","max_completion_tokens":123}`, `{"thinking":"disabled","max_tokens":123}`},
		{"codex", `{"reasoning":{"effort":"high"},"max_output_tokens":456}`, `{"max_tokens":456}`},
	} {
		data, err := json.Marshal(extractRequestSettings([]byte(tc.body), tc.format))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != tc.want {
			t.Fatalf("%s: got %s, want %s", tc.body, data, tc.want)
		}
	}
}

func TestReportedReasoningTokens(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{"claude", `{"usage":{"output_tokens":1,"output_tokens_details":{"thinking_tokens":0}}}`},
		{"openai", `{"usage":{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0}}}`},
		{"codex", `{"response":{"usage":{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0}}}}`},
		{"gemini", `{"usageMetadata":{"candidatesTokenCount":1,"thoughtsTokenCount":0}}`},
	} {
		detail := ParsePluginExecutorResponseUsage(tc.protocol, []byte(tc.body))
		if !detail.ReasoningTokensReported || detail.ReasoningTokens != 0 {
			t.Fatalf("%s: %+v", tc.protocol, detail)
		}
		merged := MergeStreamUsageDetail(detail, usage.Detail{OutputTokens: 2})
		if !merged.ReasoningTokensReported {
			t.Fatalf("%s lost presence on merge", tc.protocol)
		}
		merged = MergeStreamUsageDetail(usage.Detail{ReasoningTokens: 10, ReasoningTokensReported: true}, detail)
		if merged.ReasoningTokens != 0 {
			t.Fatalf("%s lost explicit zero", tc.protocol)
		}
	}
	for _, protocol := range []string{"claude", "openai", "codex", "gemini"} {
		if detail := ParsePluginExecutorResponseUsage(protocol, []byte(`{"usage":{"output_tokens":1},"usageMetadata":{"candidatesTokenCount":1}}`)); detail.ReasoningTokensReported {
			t.Fatalf("%s invented presence", protocol)
		}
	}
}

func TestReportedZeroReasoningSurvivesStreamBuffer(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{"claude", `data: {"usage":{"output_tokens":0,"output_tokens_details":{"thinking_tokens":0}}}`},
		{"openai", `data: {"service_tier":"default","usage":{"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0}}}`},
		{"codex", `data: {"response":{"service_tier":"default","usage":{"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0}}}}`},
		{"gemini", `data: {"usageMetadata":{"candidatesTokenCount":0,"thoughtsTokenCount":0}}`},
	} {
		buffer := &StreamUsageBuffer{}
		ObservePluginExecutorStreamUsage(tc.protocol, []byte(tc.body+"\n\n"), buffer)
		detail, ok := buffer.Detail()
		if !ok || !detail.ReasoningTokensReported || detail.ReasoningTokens != 0 {
			t.Fatalf("%s: ok=%v detail=%+v", tc.protocol, ok, detail)
		}
	}
}
