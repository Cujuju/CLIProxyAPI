package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type upstreamIDExecutor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func TestExecutorsCaptureUpstreamID(t *testing.T) {
	cfg := &config.Config{}
	for _, tc := range []struct {
		name, model, id, request, body, events string
		format                                 sdktranslator.Format
		executor                               upstreamIDExecutor
	}{
		{"claude", "claude-sonnet-4-5", "msg_test",
			`{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			`{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_test","type":"message","model":"claude-sonnet-4-5","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
			sdktranslator.FormatClaude, NewClaudeExecutor(cfg)},
		{"gemini", "gemini-2.5-flash", "gem_test",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			`{"responseId":"gem_test","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
			"", sdktranslator.FormatGemini, NewGeminiExecutor(cfg)},
		{"openai-chat", "gpt-4o", "chatcmpl-test",
			`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			`{"id":"chatcmpl-test","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			"", sdktranslator.FormatOpenAI, NewOpenAICompatExecutor("openai", cfg)},
		{"codex", "gpt-5.5", "resp_test",
			`{"model":"gpt-5.5","input":"hi"}`,
			"",
			"data: " + `{"type":"response.created","response":{"id":"resp_test","model":"gpt-5.5"}}` + "\n\ndata: " + `{"type":"response.completed","response":{"id":"resp_test","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n",
			sdktranslator.FormatOpenAIResponse, NewCodexExecutor(cfg)},
	} {
		for _, stream := range []bool{false, true} {
			name := tc.name + "/nonstream"
			if stream {
				name = tc.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream || tc.name == "codex" {
						w.Header().Set("Content-Type", "text/event-stream")
						events := tc.events
						if events == "" {
							events = "data: " + tc.body + "\n\ndata: [DONE]\n\n"
						}
						_, _ = io.WriteString(w, events)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, tc.body)
					}
				}))
				defer server.Close()
				capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
				ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
				auth := &cliproxyauth.Auth{ID: "test-auth", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.request)}
				opts := cliproxyexecutor.Options{SourceFormat: tc.format}
				if stream {
					result, err := tc.executor.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, err := tc.executor.Execute(ctx, auth, req, opts); err != nil {
						t.Fatal(err)
					}
				}
				if record := capture.await(t); record.UpstreamID != tc.id {
					t.Fatalf("ID=%q, want %q (failed=%v)", record.UpstreamID, tc.id, record.Failed)
				}
			})
		}
	}
}
