package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestQuotaStateObserveResponseHeadersIgnoresOlderSnapshot(t *testing.T) {
	newer, older := time.Unix(2_000, 0), time.Unix(1_000, 0)
	var quota QuotaState
	quota.ObserveResponseHeadersForProvider("claude", http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"}}, newer)
	if quota.ObserveResponseHeadersForProvider("claude", http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"}}, older) {
		t.Fatal("an older snapshot replaced a newer one")
	}
	if got := quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"]; got != "rejected" || !quota.ObservedAt.Equal(newer) {
		t.Fatalf("snapshot = %q at %v, want rejected at %v", got, quota.ObservedAt, newer)
	}
}

// A stream whose headers arrived before the credential's 429 finishes after it. Its success must
// not restore the pre-limit snapshot, or the spent pool stops answering as a usage limit and
// clients lose the reset time.
func TestMarkResult_StreamFinishingAfterLimitKeepsRejectedSnapshot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := registerExhaustionAuth(t, &Auth{ID: "order-claude", Provider: "claude", Status: StatusActive})
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	windowHeaders := func(status, utilization string) http.Header {
		return http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      {status},
			"Anthropic-Ratelimit-Unified-5h-Utilization": {utilization},
			"Anthropic-Ratelimit-Unified-5h-Reset":       {reset},
		}
	}

	streamCtx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(streamCtx, windowHeaders("allowed_warning", "0.98"))
	// Distinct arrival instants even on coarse clocks.
	time.Sleep(time.Millisecond)

	limitCtx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(limitCtx, windowHeaders("rejected", "1.0"))
	manager.MarkResult(limitCtx, Result{
		AuthID: auth.ID, Provider: "claude", Model: exhaustionTestModel,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`},
	})
	manager.MarkResult(streamCtx, Result{AuthID: auth.ID, Provider: "claude", Model: exhaustionTestModel, Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth missing")
	}
	if got := updated.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"]; got != "rejected" {
		t.Fatalf("5h status = %q, want rejected", got)
	}
	if _, exhausted := credentialQuotaLimit(updated, exhaustionTestModel, time.Now()); !exhausted {
		t.Fatal("credential no longer reads as exhausted")
	}
}
