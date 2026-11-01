package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
			var notified atomic.Int32
			unsubscribe := m.SubscribeQuotaUpdates(func() { notified.Add(1) })
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
			if notified.Load() != 1 {
				t.Fatalf("usage notifications = %d, want 1", notified.Load())
			}
			if provider == "codex-websocket" {
				logging.MergeResponseHeaders(upstreamCtx, http.Header{"X-Codex-Primary-Used-Percent": {"43"}})
				current, _ = m.GetByID(a.ID)
				windows = QuotaWindows(current, time.Now())
				if *windows[0].UsedPercent != 43 || notified.Load() != 2 {
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

func TestStreamQuotaRequestEndObservationContract(t *testing.T) {
	for _, mode := range []string{"unchanged headers", "Home ephemeral", "stale credential", "Background"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a, err := m.Register(context.Background(), &Auth{ID: "request-end", Provider: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "Home ephemeral" {
				m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "Background" {
				ctx = context.Background()
			}
			ctx = m.observeStreamQuota(logging.WithFreshResponseHeadersHolder(ctx), a, nil)
			if mode == "stale credential" {
				m.mu.Lock()
				m.auths[a.ID].RegistrationEpoch++
				m.mu.Unlock()
			}
			// Empty headers leave quota unchanged but still record response activity for a current credential.
			logging.SetResponseHeaders(ctx, http.Header{})
			logging.SetResponseHeaders(ctx, http.Header{})
			var notifications atomic.Int32
			notified := make(chan struct{}, 4)
			stop := m.SubscribeQuotaUpdates(func() {
				m.GetByID(a.ID)
				notifications.Add(1)
				notified <- struct{}{}
			})
			defer stop()
			endedAfter := time.Now()
			cancel()
			if mode == "unchanged headers" {
				select {
				case <-notified:
				case <-time.After(time.Second):
					t.Fatal("unchanged headers did not register request end")
				}
				current, _ := m.GetByID(a.ID)
				if current.Quota.ResponseEndedAt.Before(endedAfter) {
					t.Fatal("missing request-end stamp")
				}
				if notifications.Load() != 1 {
					t.Fatal("request end notified more than once")
				}
			} else {
				select {
				case <-notified:
					t.Fatal("ineligible observation notified request end")
				case <-time.After(20 * time.Millisecond):
				}
				current, _ := m.GetByID(a.ID)
				if !current.Quota.ResponseEndedAt.IsZero() || notifications.Load() != 0 {
					t.Fatal("ineligible observation stamped request end")
				}
			}
		})
	}
}
