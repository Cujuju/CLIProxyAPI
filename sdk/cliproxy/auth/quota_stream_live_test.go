package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestStreamQuotaVisibleBeforeCompletion(t *testing.T) {
	for _, provider := range []string{"claude", "codex-http", "codex-websocket"} {
		t.Run(provider, func(t *testing.T) {
			name := "codex"
			if provider == "claude" {
				name = "claude"
			}
			m := NewManager(nil, nil, nil)
			a, err := m.Register(context.Background(), &Auth{ID: "stream-quota", Provider: name})
			if err != nil {
				t.Fatal(err)
			}
			notified := 0
			unsubscribe := m.SubscribeQuotaUpdates(func() { notified++ })
			defer unsubscribe()
			ctx, cancel := context.WithCancel(logging.WithFreshResponseHeadersHolder(context.Background()))
			defer cancel()
			chunks := make(chan cliproxyexecutor.StreamChunk)
			var upstreamCtx context.Context
			executor := &customStreamMockExecutor{identifier: name, streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				upstreamCtx = ctx
				h := http.Header{"X-Codex-Primary-Used-Percent": {"42"}}
				if name == "claude" {
					h = http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.42"}}
				}
				if provider == "codex-websocket" {
					logging.MergeResponseHeaders(ctx, h)
				} else {
					logging.SetResponseHeaders(ctx, h)
				}
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			}}
			_, err = m.executeStreamAttempt(ctx, executor, a, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			// The upstream is still open; no execution result or request event exists yet.
			current, _ := m.GetByID(a.ID)
			windows := QuotaWindows(current, time.Now())
			if len(windows) != 1 || windows[0].UsedPercent == nil || *windows[0].UsedPercent != 42 {
				t.Fatalf("open stream quota = %+v, want 42%%", windows)
			}
			if notified != 1 {
				t.Fatalf("usage notifications = %d, want 1", notified)
			}
			if provider == "codex-websocket" {
				logging.MergeResponseHeaders(upstreamCtx, http.Header{"X-Codex-Primary-Used-Percent": {"43"}})
				current, _ = m.GetByID(a.ID)
				windows = QuotaWindows(current, time.Now())
				if *windows[0].UsedPercent != 43 || notified != 2 {
					t.Fatal("late websocket quota did not publish while the stream remained open")
				}
			}
			cancel()
			close(chunks)
			current, _ = m.GetByID(a.ID)
			if len(QuotaWindows(current, time.Now())) != 1 {
				t.Fatal("cancellation lost quota")
			}
		})
	}
}
func TestStreamQuotaIgnoresReplacedCredential(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), &Auth{ID: "replaced", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := m.observeStreamQuota(logging.WithFreshResponseHeadersHolder(context.Background()), a, nil)
	m.mu.Lock()
	m.auths[a.ID].RegistrationEpoch++
	m.mu.Unlock()
	logging.SetResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"42"}})
	current, _ := m.GetByID(a.ID)
	if len(current.Quota.Signals) != 0 {
		t.Fatal("old stream updated a replacement credential")
	}
}
