package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
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

func TestAntigravityQuotaLookupSurvivesResponseAndObservationMerge(t *testing.T) {
	lookedUpAt := time.Unix(2_000, 0)
	window := QuotaWindow{Key: "ag_gemini", Scoped: true}
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(t.Context(), &Auth{
		ID: "quota-antigravity", Provider: "antigravity", Status: StatusActive,
		Quota: QuotaState{LookedUpAt: lookedUpAt, LookupWindows: []QuotaWindow{window}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(t.Context(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "gemini-pro", Success: true})
	stored, ok := manager.GetByID(auth.ID)
	if !ok || !stored.Quota.LookedUpAt.Equal(lookedUpAt) || len(stored.Quota.LookupWindows) != 1 || stored.Quota.LookupWindows[0] != window {
		t.Fatalf("response changed the lookup snapshot: %+v", stored)
	}

	stale := QuotaState{
		LookedUpAt: lookedUpAt.Add(-time.Second), LookupWindows: []QuotaWindow{{Key: "stale"}},
		Exceeded: true,
	}
	applyObservationFields(&stale, mergeQuotaObservation(stale, stored.Quota))
	if !stale.LookedUpAt.Equal(lookedUpAt) || len(stale.LookupWindows) != 1 || stale.LookupWindows[0] != window || !stale.Exceeded {
		t.Fatalf("expected complete newer snapshot and preserved cooldown, got %+v", stale)
	}
	stale.LookupWindows[0].Key = "modified"
	if stored.Quota.LookupWindows[0] != window {
		t.Fatal("merged lookup windows alias the source")
	}
}

func TestObserveQuotaLookupWindows(t *testing.T) {
	now := time.Unix(2_000, 0)
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(t.Context(), &Auth{
		ID: "lookup-windows", Provider: "claude", Status: StatusActive,
		Quota: QuotaState{LookedUpAt: now, LookupSignals: map[string]string{"anthropic-ratelimit-unified-status": QuotaWindowStatusAllowed}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var published []QuotaState
	defer manager.SubscribeQuotaUpdates(func() {
		current, _ := manager.GetByID(auth.ID)
		published = append(published, current.Quota)
	})()

	used := 10.0
	windows := []QuotaWindow{{Key: "five_hour", UsedPercent: &used}}
	called := false
	recorded, err := manager.ObserveQuotaLookupWindows(nil, auth.ID, windows, now, func(current *Auth) (bool, error) {
		called = true
		if current.ID != auth.ID || len(current.Quota.LookupSignals) != 1 {
			t.Errorf("accept saw unexpected live credential: %+v", current)
		}
		return true, nil
	})
	if !called || !recorded || err != nil || len(published) != 1 {
		t.Fatalf("tie: accept called=%t, recorded=%t, err=%v, publications=%d", called, recorded, err, len(published))
	}
	windows[0].Key = "changed"
	used = 20
	current, _ := manager.GetByID(auth.ID)
	if !reflect.DeepEqual(current.Quota, published[0]) || !current.Quota.LookedUpAt.Equal(now) ||
		current.Quota.LookupSignals != nil || len(current.Quota.LookupWindows) != 1 ||
		current.Quota.LookupWindows[0].Key != "five_hour" || *current.Quota.LookupWindows[0].UsedPercent != 10 {
		t.Fatalf("unexpected recorded snapshot: %+v", current.Quota)
	}

	rejected := errors.New("rejected by lookup")
	for _, tc := range []struct {
		name   string
		at     time.Time
		accept func(*Auth) (bool, error)
		err    error
	}{
		{"older with nil accept", now.Add(-time.Second), nil, nil},
		{"accept skip", now, func(*Auth) (bool, error) { return false, nil }, nil},
		{"older accept error", now.Add(-time.Second), func(*Auth) (bool, error) { return true, rejected }, rejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, windows, tc.at, tc.accept)
			current, _ := manager.GetByID(auth.ID)
			if recorded || !errors.Is(err, tc.err) || len(published) != 1 || !reflect.DeepEqual(current.Quota, published[0]) {
				t.Fatalf("recorded=%t, err=%v, publications=%d, quota=%+v", recorded, err, len(published), current.Quota)
			}
		})
	}
	recorded, err = manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, windows, now.Add(-time.Second), func(*Auth) (bool, error) {
		return true, nil
	})
	current, _ = manager.GetByID(auth.ID)
	if !recorded || err != nil || len(published) != 2 || !reflect.DeepEqual(current.Quota, published[1]) ||
		!current.Quota.LookedUpAt.Equal(now) || !reflect.DeepEqual(current.Quota.LookupWindows, windows) {
		t.Fatalf("accepted older lookup: recorded=%t, err=%v, publications=%d, quota=%+v", recorded, err, len(published), current.Quota)
	}

	for _, at := range []time.Time{now, now.Add(time.Second)} {
		publications := len(published)
		recorded, err = manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, windows, at, nil)
		current, _ = manager.GetByID(auth.ID)
		if !recorded || err != nil || len(published) != publications+1 || !current.Quota.LookedUpAt.Equal(at) {
			t.Fatalf("nil accept at %v: recorded=%t, err=%v, publications=%d, quota=%+v", at, recorded, err, len(published), current.Quota)
		}
	}
}

func TestObserveQuotaLookupWindowsValidation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(t.Context(), &Auth{ID: "lookup-input"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000, 0)
	windows := []QuotaWindow{{Key: "window"}}
	for _, tc := range []struct {
		id      string
		windows []QuotaWindow
		at      time.Time
	}{
		{" ", windows, now},
		{auth.ID, nil, now},
		{auth.ID, windows, time.Time{}},
	} {
		if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), tc.id, tc.windows, tc.at, nil); recorded || err == nil {
			t.Errorf("invalid input %+v: recorded=%t, err=%v", tc, recorded, err)
		}
	}
	if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), "missing", windows, now, nil); recorded || !errors.Is(err, ErrQuotaLookupCredentialChanged) {
		t.Errorf("missing credential: recorded=%t, err=%v", recorded, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if recorded, err := manager.ObserveQuotaLookupWindows(ctx, auth.ID, windows, now, nil); recorded || !errors.Is(err, ctx.Err()) {
		t.Errorf("canceled lookup: recorded=%t, err=%v", recorded, err)
	}
	current, _ := manager.GetByID(auth.ID)
	if !current.Quota.LookedUpAt.IsZero() {
		t.Fatalf("failed lookup changed the snapshot: %+v", current.Quota)
	}
}

func TestObserveQuotaLookupWindowsEmptyReplacesWithoutLiftingCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Now()
	full := 100.0
	auth := &Auth{
		ID: "empty-clear", Provider: "codex", UpdatedAt: now, Status: StatusError,
		Unavailable: true, NextRetryAfter: now.Add(time.Hour),
		Quota: QuotaState{
			Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: now.Add(time.Hour),
			LookedUpAt: now, LookupWindows: []QuotaWindow{{Key: "primary", UsedPercent: &full}},
		},
	}
	manager.auths[auth.ID] = auth
	at := now.Add(time.Second)
	if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, []QuotaWindow{}, at, nil); !recorded || err != nil {
		t.Fatalf("empty lookup recorded=%v err=%v", recorded, err)
	}
	after, _ := manager.GetByID(auth.ID)
	if len(QuotaWindows(after, at)) != 0 || !after.Quota.LookedUpAt.Equal(at) {
		t.Fatalf("empty lookup did not replace prior windows: %+v", after.Quota)
	}
	if !after.Quota.Exceeded || !after.Unavailable || !after.NextRetryAfter.Equal(now.Add(time.Hour)) {
		t.Fatalf("empty lookup lifted cooldown: %+v", after)
	}
}

func TestObserveQuotaLookupWindowsBoundsRetainedSnapshot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(t.Context(), &Auth{ID: "bounded", Provider: "claude", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000, 0)
	windows := make([]QuotaWindow, 0, 100)
	for i := range 98 {
		used := float64(i % 100)
		windows = append(windows, QuotaWindow{
			Key:         "a_" + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			UsedPercent: &used,
			Scoped:      true,
		})
	}
	used5h, used7d := 12.0, 34.0
	windows = append(windows,
		QuotaWindow{Key: "seven_day", Duration: weeklyWindow, UsedPercent: &used7d},
		QuotaWindow{Key: "five_hour", Duration: fiveHourWindow, UsedPercent: &used5h},
	)
	if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, windows, now, nil); !recorded || err != nil {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	stored, _ := manager.GetByID(auth.ID)
	if len(stored.Quota.LookupWindows) != maxQuotaLookupWindows {
		t.Fatalf("retained %d windows, want %d", len(stored.Quota.LookupWindows), maxQuotaLookupWindows)
	}
	manager.mu.RLock()
	liveCap := cap(manager.auths[auth.ID].Quota.LookupWindows)
	manager.mu.RUnlock()
	if liveCap != maxQuotaLookupWindows {
		t.Fatalf("live backing array cap = %d, want %d", liveCap, maxQuotaLookupWindows)
	}
	if stored.Quota.LookupWindows[0].Key != "five_hour" || stored.Quota.LookupWindows[1].Key != "seven_day" {
		t.Fatalf("account-wide windows not prioritized: %+v", stored.Quota.LookupWindows[:2])
	}
	if stored.Quota.LookupWindows[2].Key != "a_00" || stored.Quota.LookupWindows[maxQuotaLookupWindows-1].Key != "a_61" {
		t.Fatalf("scoped retention not deterministic: first=%q last=%q",
			stored.Quota.LookupWindows[2].Key, stored.Quota.LookupWindows[maxQuotaLookupWindows-1].Key)
	}
}

func TestObserveQuotaLookupWindowsTruncationPreservesCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Unix(2_000, 0)
	auth := &Auth{
		ID: "truncated-cooldown", Provider: "claude", UpdatedAt: now, Status: StatusError,
		Unavailable: true, NextRetryAfter: now.Add(time.Hour),
		Quota: QuotaState{Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: now.Add(time.Hour)},
	}
	manager.auths[auth.ID] = auth
	windows := make([]QuotaWindow, 0, maxQuotaLookupWindows+1)
	for i := range maxQuotaLookupWindows {
		avail := 10.0
		windows = append(windows, QuotaWindow{
			Key:         "a_" + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			Duration:    fiveHourWindow,
			UsedPercent: &avail,
		})
	}
	full := 100.0
	weeklyReset := now.Add(48 * time.Hour)
	windows = append(windows, QuotaWindow{Key: "seven_day", Duration: weeklyWindow, UsedPercent: &full, ResetsAt: weeklyReset})
	if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, windows, now.Add(time.Second), nil); !recorded || err != nil {
		t.Fatalf("recorded=%v err=%v", recorded, err)
	}
	after, _ := manager.GetByID(auth.ID)
	if len(after.Quota.LookupWindows) != maxQuotaLookupWindows {
		t.Fatalf("retained %d windows, want %d", len(after.Quota.LookupWindows), maxQuotaLookupWindows)
	}
	if !after.Quota.Exceeded || !after.Unavailable || !after.NextRetryAfter.Equal(now.Add(time.Hour)) {
		t.Fatalf("truncated exhausted account window cleared cooldown: %+v", after)
	}
	limit, ok := credentialQuotaLimit(after, "", now.Add(time.Second))
	if !ok || !limit.ResetsAt.Equal(weeklyReset) || limit.Window != weeklyWindow {
		t.Fatalf("credentialQuotaLimit = %+v, %v; want %v (%v)", limit, ok, weeklyReset, weeklyWindow)
	}

	// Active reset tie: 64 spent weekly windows + 1 spent 5-hour window sharing the same reset.
	tieReset := now.Add(4 * time.Hour)
	tieWindows := make([]QuotaWindow, 0, maxQuotaLookupWindows+1)
	for i := range maxQuotaLookupWindows {
		tieWindows = append(tieWindows, QuotaWindow{
			Key:         "w_" + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			Duration:    weeklyWindow,
			UsedPercent: &full,
			ResetsAt:    tieReset,
		})
	}
	tieWindows = append(tieWindows, QuotaWindow{Key: "five_hour", Duration: fiveHourWindow, UsedPercent: &full, ResetsAt: tieReset})
	if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, tieWindows, now.Add(2*time.Second), nil); !recorded || err != nil {
		t.Fatalf("tie lookup recorded=%v err=%v", recorded, err)
	}
	after, _ = manager.GetByID(auth.ID)
	if limit, ok = credentialQuotaLimit(after, "", now.Add(2*time.Second)); !ok || !limit.ResetsAt.Equal(tieReset) || limit.Window != fiveHourWindow {
		t.Fatalf("active tie credentialQuotaLimit = %+v, %v; want %v (%v)", limit, ok, tieReset, fiveHourWindow)
	}

	// All windows lapsed during active cooldown: 64 recently lapsed 5h windows + 1 earlier-lapsed weekly window.
	lapsedWindows := make([]QuotaWindow, 0, maxQuotaLookupWindows+1)
	for i := range maxQuotaLookupWindows {
		lapsedWindows = append(lapsedWindows, QuotaWindow{
			Key:         "s_" + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			Duration:    fiveHourWindow,
			UsedPercent: &full,
			ResetsAt:    now.Add(-time.Minute),
		})
	}
	lapsedWindows = append(lapsedWindows, QuotaWindow{Key: "seven_day", Duration: weeklyWindow, UsedPercent: &full, ResetsAt: now.Add(-10 * time.Minute)})
	if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, lapsedWindows, now.Add(3*time.Second), nil); !recorded || err != nil {
		t.Fatalf("lapsed lookup recorded=%v err=%v", recorded, err)
	}
	after, _ = manager.GetByID(auth.ID)
	if limit, ok = credentialQuotaLimit(after, "", now.Add(3*time.Second)); !ok || !limit.ResetsAt.Equal(now.Add(time.Hour)) || limit.Window != weeklyWindow {
		t.Fatalf("lapsed fallback credentialQuotaLimit = %+v, %v; want %v (%v)", limit, ok, now.Add(time.Hour), weeklyWindow)
	}
}

func TestLookupRecoveryRequiresKnownAvailableAccountWindows(t *testing.T) {
	available, full := 5.0, 100.0
	first := QuotaWindow{Key: "five_hour", UsedPercent: &available}
	for _, tc := range []struct {
		name    string
		windows []QuotaWindow
		headers http.Header
	}{
		{"scoped-only", []QuotaWindow{{Key: "model", UsedPercent: &available, Scoped: true}},
			http.Header{"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"0.05"}}},
		{"rejected", []QuotaWindow{first, {Key: "weekly", UsedPercent: &available, Status: QuotaWindowStatusRejected}},
			http.Header{"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"}}},
		{"full", []QuotaWindow{first, {Key: "weekly", UsedPercent: &full}},
			http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"1"}}},
		{"unknown", []QuotaWindow{first, {Key: "weekly"}},
			http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {"2000000000"}}},
	} {
		for _, source := range []string{"windows", "signals"} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				now := time.Now()
				auth := &Auth{ID: "lookup-gate", Provider: "claude", UpdatedAt: now, Status: StatusError,
					Unavailable: true, NextRetryAfter: now.Add(time.Hour),
					Quota: QuotaState{Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: now.Add(time.Hour)}}
				manager.auths[auth.ID] = auth
				at := now.Add(time.Second)
				if source == "windows" {
					if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, tc.windows, at, nil); !recorded || err != nil {
						t.Fatalf("lookup recorded = %v, err = %v", recorded, err)
					}
				} else {
					headers := tc.headers.Clone()
					if tc.name != "scoped-only" {
						headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.05")
					}
					if !manager.ObserveQuotaLookup(auth.ID, headers, at) {
						t.Fatal("lookup not recorded")
					}
				}
				after, _ := manager.GetByID(auth.ID)
				if !after.Quota.Exceeded || !after.Unavailable || !after.NextRetryAfter.Equal(now.Add(time.Hour)) {
					t.Fatalf("lookup lifted the credential cooldown: %+v", after)
				}
			})
		}
	}
}

func TestQuotaLookupSurvivesCredentialScopedRateLimit(t *testing.T) {
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	const model = "claude-sonnet-4-6"
	snapshot := QuotaState{
		LookedUpAt:    time.Unix(2_000, 0),
		LookupWindows: []QuotaWindow{{Key: model, Scoped: true}},
		LookupSignals: map[string]string{"anthropic-ratelimit-unified-status": QuotaWindowStatusAllowed},
	}
	auth, err := manager.Register(t.Context(), &Auth{
		ID: "lookup-with-model-state", Provider: "claude", Status: StatusActive,
		ModelStates: map[string]*ModelState{model: {Status: StatusActive}},
		Quota:       snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	retry := time.Hour
	manager.MarkResult(t.Context(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model, CredentialScope: true,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Code: ErrorCodeCredentialQuota},
		RetryAfter: &retry,
	})
	stored, _ := manager.GetByID(auth.ID)
	if stored.Quota.Reason != "credential_quota" || !stored.Quota.LookedUpAt.Equal(snapshot.LookedUpAt) ||
		!reflect.DeepEqual(stored.Quota.LookupWindows, snapshot.LookupWindows) ||
		!reflect.DeepEqual(stored.Quota.LookupSignals, snapshot.LookupSignals) {
		t.Fatalf("credential quota changed the lookup snapshot: %+v", stored.Quota)
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
