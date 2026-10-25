package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func pickID(t *testing.T, m *Manager) string {
	t.Helper()
	auth, _, _, err := m.pickNextMixed(context.Background(), []string{"codex"}, "gpt-5.6-terra", cliproxyexecutor.Options{}, map[string]struct{}{})
	if err != nil || auth == nil {
		t.Fatalf("pick: %v", err)
	}
	return auth.ID
}

func TestPreferredAuth_OverridesAndFallsBack(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 2)
	m.RegisterExecutor(&issue6865Executor{id: "codex"})
	if got := pickID(t, m); got != ids[0] {
		t.Fatalf("automatic pick = %s, want %s", got, ids[0])
	}

	m.SetPreferredAuth("codex", ids[1])
	if got := pickID(t, m); got != ids[1] {
		t.Fatalf("preferred pick = %s, want %s", got, ids[1])
	}

	// A cooling-down preferred credential falls back to automatic routing.
	retryAfter := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: ids[1], Provider: "codex", Model: "gpt-5.6-terra", Success: false, RetryAfter: &retryAfter,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "limit"}})
	if got := pickID(t, m); got != ids[0] {
		t.Fatalf("pick with preferred cooling down = %s, want fallback %s", got, ids[0])
	}

	m.SetPreferredAuth("codex", "")
	if len(m.PreferredAuths()) != 0 {
		t.Fatalf("preference not cleared: %v", m.PreferredAuths())
	}
}

func TestPreferredAuth_RequestPinWins(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 2)
	m.RegisterExecutor(&issue6865Executor{id: "codex"})
	m.SetPreferredAuth("codex", ids[1])
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: ids[0]}}
	auth, _, _, err := m.pickNextMixed(context.Background(), []string{"codex"}, "gpt-5.6-terra", opts, map[string]struct{}{})
	if err != nil || auth == nil || auth.ID != ids[0] {
		t.Fatalf("request pin overridden: auth=%v err=%v", auth, err)
	}
}
