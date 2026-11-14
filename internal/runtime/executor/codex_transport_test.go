package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type transportUsageSink struct {
	authID  string
	records chan usage.Record
}

func (s *transportUsageSink) HandleUsage(_ context.Context, r usage.Record) {
	if r.AuthID == s.authID {
		select {
		case s.records <- r:
		default:
		}
	}
}
func newTransportSink(t *testing.T) *transportUsageSink {
	t.Helper()
	s := &transportUsageSink{t.Name(), make(chan usage.Record, 32)}
	usage.RegisterNamedPlugin(t.Name(), s)
	return s
}
func (s *transportUsageSink) drain(t *testing.T) []usage.Record {
	t.Helper()
	usage.PublishRecord(context.Background(), usage.Record{AuthID: s.authID, RequestID: "transport-test-barrier"})
	var records []usage.Record
	for {
		select {
		case r := <-s.records:
			if r.RequestID == "transport-test-barrier" {
				return records
			}
			records = append(records, r)
		case <-time.After(5 * time.Second):
			t.Fatal("usage barrier timed out")
			return nil
		}
	}
}

const transportCompleted = `{"type":"response.completed","response":{"id":"resp_transport","model":"gpt-test","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`

func transportInbound(ws bool) context.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), "gin", c)
	if ws {
		ctx = core.WithDownstreamWebsocket(ctx)
	}
	return ctx
}
func transportAuth(id, base string) *auth.Auth {
	return &auth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": base, "websockets": "true"}}
}
func transportRequest() core.Request {
	return core.Request{Model: "gpt-test", Payload: []byte(`{"model":"gpt-test","input":[]}`)}
}
func transportOptions() core.Options {
	return core.Options{SourceFormat: translator.FromString("codex")}
}

func TestCodexObservedTransportMatrix(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, scenario := range []struct {
			name                       string
			downstreamWS, explicitHTTP bool
			reject                     int
			httpFailure                bool
			upstream, reason           string
		}{
			{"http_to_ws", false, false, 0, false, "websocket", ""},
			{"ws_to_ws", true, false, 0, false, "websocket", ""},
			{"explicit_http", false, true, 0, false, "http", ""},
			{"ws_to_http", true, true, 0, false, "http", ""},
			{"upgrade_rejected", false, false, 401, false, "websocket", "websocket_upgrade_401"},
			{"upgrade_fallback", false, false, 426, false, "websocket+http", "websocket_upgrade_426"},
			{"fallback_failed", false, false, 426, true, "websocket+http", "websocket_upgrade_426"},
			{"downstream_ws_no_replay", true, false, 426, false, "websocket", "websocket_upgrade_426"},
		} {
			t.Run(fmt.Sprintf("%s/stream_%t", scenario.name, streaming), func(t *testing.T) {
				sink := newTransportSink(t)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if websocket.IsWebSocketUpgrade(r) {
						if scenario.reject != 0 {
							http.Error(w, "rejected", scenario.reject)
							return
						}
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.Close() }()
						if _, _, err = conn.ReadMessage(); err != nil {
							t.Error(err)
							return
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(transportCompleted))
						return
					}
					if scenario.httpFailure {
						http.Error(w, "http failed", 500)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", transportCompleted)
				}))
				defer server.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
				wsExec := NewCodexWebsocketsExecutor(cfg)
				var exec interface {
					Execute(context.Context, *auth.Auth, core.Request, core.Options) (core.Response, error)
					ExecuteStream(context.Context, *auth.Auth, core.Request, core.Options) (*core.StreamResult, error)
				} = wsExec
				if scenario.explicitHTTP {
					exec = NewCodexExecutor(cfg)
				}
				ctx := transportInbound(scenario.downstreamWS)
				credential := transportAuth(t.Name(), server.URL)
				if streaming {
					result, _ := exec.ExecuteStream(ctx, credential, transportRequest(), transportOptions())
					if result != nil {
						for range result.Chunks {
						}
					}
				} else {
					_, _ = exec.Execute(ctx, credential, transportRequest(), transportOptions())
				}
				records := sink.drain(t)
				if len(records) != 1 {
					t.Fatalf("records = %d, want one attempt: %+v", len(records), records)
				}
				r := records[0]
				downstream := "http"
				if scenario.downstreamWS {
					downstream = "websocket"
				}
				if r.DownstreamTransport != downstream || r.UpstreamTransport != scenario.upstream || r.TransportFallbackReason != scenario.reason {
					t.Fatalf("transport = %q / %q / %q, want %q / %q / %q", r.DownstreamTransport, r.UpstreamTransport, r.TransportFallbackReason, downstream, scenario.upstream, scenario.reason)
				}
				wantFailure := scenario.httpFailure || scenario.reject == 401 || (scenario.reject == 426 && scenario.downstreamWS)
				if r.Failed != wantFailure {
					t.Fatalf("failed = %t, want %t", r.Failed, wantFailure)
				}
			})
		}
	}
}

func TestCodexTransportReusedSessionAndCancellation(t *testing.T) {
	sink := newTransportSink(t)
	var handshakes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		handshakes.Add(1)
		for i := 0; i < 3; i++ {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
			if i == 2 {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"test"}`))
				_, _, _ = conn.ReadMessage()
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(transportCompleted))
		}
	}))
	defer server.Close()
	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	defer exec.CloseExecutionSession(t.Name())
	opts := transportOptions()
	opts.Metadata = map[string]any{core.ExecutionSessionMetadataKey: t.Name()}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(transportInbound(false))
		result, err := exec.ExecuteStream(ctx, transportAuth(t.Name(), server.URL), transportRequest(), opts)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for range result.Chunks {
			if i == 2 {
				cancel()
			}
		}
		cancel()
	}
	records := sink.drain(t)
	// Client cancellation follows the HTTP policy: no extra failure or success is counted.
	if len(records) != 2 || handshakes.Load() != 1 {
		t.Fatalf("records=%d handshakes=%d", len(records), handshakes.Load())
	}
	for i, r := range records {
		if r.UpstreamTransport != "websocket" || r.DownstreamTransport != "http" || r.TransportFallbackReason != "" || r.Failed {
			t.Fatalf("attempt %d = %+v", i, r)
		}
		if i > 0 && records[i-1].RequestID == r.RequestID {
			t.Fatal("reused attempt identity")
		}
	}
}

func TestCodexTransportDuplexPartialSuccessor(t *testing.T) {
	for _, scenario := range []string{"automatic_cancel", "pending_disconnect", "pending_credential_failure"} {
		t.Run(scenario, func(t *testing.T) {
			pendingDisconnect := scenario != "automatic_cancel"
			credentialFailure := scenario == "pending_credential_failure"
			sink := newTransportSink(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"first","output":[]}}`))
				if pendingDisconnect {
					if _, _, err = conn.ReadMessage(); err != nil {
						return
					}
				}
				if credentialFailure {
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":429,"response_id":"first","error":{"type":"usage_limit_reached","message":"quota"}}`))
					_, _, _ = conn.ReadMessage()
					return
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"first","output":[]}}`))
				if pendingDisconnect {
					return
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"successor","previous_response_id":"first","output":[]}}`))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"partial"}`))
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(transportInbound(true))
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			ctx = core.WithWebsocketInput(ctx, input)
			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			defer exec.CloseExecutionSession(t.Name())
			opts := transportOptions()
			opts.Metadata = map[string]any{core.ExecutionSessionMetadataKey: t.Name()}
			result, err := exec.ExecuteStream(ctx, transportAuth(t.Name(), server.URL), transportRequest(), opts)
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range result.Chunks {
				kind := ojson.GetBytes(chunk.Payload, "type").String()
				if pendingDisconnect && kind == "response.created" {
					input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"gpt-test","input":[]}`)}
				}
				if !pendingDisconnect && kind == "response.output_text.delta" {
					cancel()
				}
			}
			records := sink.drain(t)
			wantRecords := 2
			if !pendingDisconnect {
				wantRecords = 1
			}
			if len(records) != wantRecords {
				t.Fatalf("records=%d, want first and partial successor: %+v", len(records), records)
			}
			for i, r := range records {
				if r.DownstreamTransport != "websocket" || r.UpstreamTransport != "websocket" || r.TransportFallbackReason != "" || r.Failed != (credentialFailure || i == 1) {
					t.Fatalf("attempt %d=%+v", i, r)
				}
				if credentialFailure && r.Fail.StatusCode != 429 {
					t.Fatalf("lost failure classification: %+v", r.Fail)
				}
			}
		})
	}
}
