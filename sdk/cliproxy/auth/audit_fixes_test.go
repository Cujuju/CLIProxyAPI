package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestObserveQuotaLookupWindowsConsecutivePartialsPreserveBlocker(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	full := 100.0
	low := 10.0
	auth, err := m.Register(context.Background(), &Auth{
		ID:        "claude-partial",
		Provider:  "claude",
		Status:    StatusActive,
		UpdatedAt: now.Add(-time.Minute),
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        credentialQuotaReason,
			NextRecoverAt: now.Add(24 * time.Hour),
			LookupWindows: []QuotaWindow{
				{Key: "seven_day", UsedPercent: &full, ResetsAt: now.Add(24 * time.Hour), Status: QuotaWindowStatusRejected},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	partial := []QuotaWindow{
		{Key: "five_hour", UsedPercent: &low, ResetsAt: now.Add(5 * time.Hour), Status: QuotaWindowStatusAllowed},
	}
	// Two consecutive partial lookups omitting seven_day must neither erase seven_day nor lift cooldown.
	for i := 1; i <= 2; i++ {
		if _, errObs := m.ObserveQuotaLookupWindows(context.Background(), auth.ID, partial, now.Add(time.Duration(i)*time.Second), nil); errObs != nil {
			t.Fatalf("lookup %d: %v", i, errObs)
		}
		got, _ := m.GetByID(auth.ID)
		if !got.Quota.Exceeded {
			t.Fatalf("lookup %d lifted cooldown despite omitted seven_day blocker", i)
		}
	}
}

func TestPoolQuotaLimitCombinesCredentialRetryAfterWithRegisteredModel(t *testing.T) {
	const model = "claude-audit-test-model"
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	quotaReset := now.Add(time.Minute)
	credRetry := now.Add(time.Hour)
	full := 100.0
	auth := &Auth{
		ID:                   "cred-retry-after",
		Provider:             "claude",
		Status:               StatusActive,
		NextRetryAfter:       credRetry,
		credentialRetryAfter: credRetry,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        credentialQuotaReason,
			NextRecoverAt: quotaReset,
			LookupWindows: []QuotaWindow{
				{Key: "five_hour", UsedPercent: &full, ResetsAt: quotaReset, Status: QuotaWindowStatusRejected},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	limit, ok := poolQuotaLimit([]*Auth{auth}, []string{"claude"}, model, now)
	if !ok {
		t.Fatal("expected poolQuotaLimit to report exhaustion")
	}
	if !limit.ResetsAt.Equal(credRetry) {
		t.Fatalf("limit.ResetsAt = %v, want credentialRetryAfter %v", limit.ResetsAt, credRetry)
	}
}

func TestSessionHoldBypassedByPreferredAndPinnedAuthThroughPickNextMixed(t *testing.T) {
	const model = "claude-hold-bypass-model"
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &FillFirstSelector{},
		TTL:      time.Hour,
		Hold:     30 * time.Minute,
	})
	t.Cleanup(selector.Stop)
	m := NewManager(nil, selector, nil)
	m.RegisterExecutor(&customStreamMockExecutor{identifier: "claude"})

	now := time.Now()
	full := 100.0
	spent, err := m.Register(context.Background(), &Auth{
		ID:       "auth-spent",
		Provider: "claude",
		Status:   StatusActive,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        credentialQuotaReason,
			NextRecoverAt: now.Add(2 * time.Minute),
			LookupWindows: []QuotaWindow{
				{Key: "five_hour", UsedPercent: &full, ResetsAt: now.Add(2 * time.Minute), Status: QuotaWindowStatusRejected},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := m.Register(context.Background(), &Auth{
		ID:       "auth-fresh",
		Provider: "claude",
		Status:   StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(spent.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(fresh.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(spent.ID)
		reg.UnregisterClient(fresh.ID)
	})

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_x_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"},"cache_control":{"type":"ephemeral","ttl":"1h"}}`),
		Metadata:        map[string]any{},
	}
	sessionKey := affinityProvider + "::claude:ac980658-63bd-4fb3-97ba-8da64cb1e344::" + canonicalModelKey(model)
	selector.RestoreSessionBindings([]SessionBindingRecord{{AuthID: spent.ID, ExpiresAt: now.Add(time.Hour), Aliases: []string{sessionKey}}})
	selector.served.record(sessionKey, now, time.Hour)

	// Without pin or preference, pickNextMixed holds and returns 429.
	if _, _, _, errHold := m.pickNextMixed(context.Background(), []string{"claude"}, model, opts, nil); errHold == nil || statusCodeFromError(errHold) != http.StatusTooManyRequests {
		t.Fatalf("expected held session 429, got %v", errHold)
	}

	// Explicit request pin bypasses hold and picks auth-fresh.
	pinnedOpts := opts
	pinnedOpts.Metadata = map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: fresh.ID}
	picked, _, _, errPin := m.pickNextMixed(context.Background(), []string{"claude"}, model, pinnedOpts, nil)
	if errPin != nil || picked == nil || picked.ID != fresh.ID {
		t.Fatalf("pinned pick = (%v, %v), want %s", picked, errPin, fresh.ID)
	}

	// Desktop preference bypasses hold and picks auth-fresh.
	m.SetPreferredAuth("claude", fresh.ID)
	picked, _, _, errPref := m.pickNextMixed(context.Background(), []string{"claude"}, model, opts, nil)
	if errPref != nil || picked == nil || picked.ID != fresh.ID {
		t.Fatalf("preferred pick = (%v, %v), want %s", picked, errPref, fresh.ID)
	}
}
