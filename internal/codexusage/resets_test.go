package codexusage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fixture struct {
	tracker *Tracker
	manager *coreauth.Manager
	now     time.Time
	calls   map[string]int
	body    string
	failing bool
	// creditCalls, creditsBody and creditsFailing are the reset-credit list's counterparts.
	creditCalls    map[string]int
	creditsBody    string
	creditsFailing bool
}

func newFixture(t *testing.T, auths ...*coreauth.Auth) *fixture {
	t.Helper()
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	f := &fixture{manager: manager, now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), calls: map[string]int{}, creditCalls: map[string]int{},
		body:        `{"plan_type":"plus","rate_limit_reset_credits":{"available_count":1,"applicable_available_count":0}}`,
		creditsBody: `{"credits":[]}`}
	f.tracker = NewTracker(manager, func() *config.Config { return cfg })
	f.tracker.spacing = 0
	f.tracker.now = func() time.Time { return f.now }
	f.tracker.fetch = func(_ context.Context, _ *config.Config, auth *coreauth.Auth, url string) ([]byte, error) {
		if url == creditsURL {
			f.creditCalls[auth.ID]++
			if f.creditsFailing {
				return nil, errors.New("credit list unavailable")
			}
			return []byte(f.creditsBody), nil
		}
		f.calls[auth.ID]++
		if f.failing {
			return nil, errors.New("upstream unavailable")
		}
		return []byte(f.body), nil
	}
	return f
}

func codexOAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "token-" + id}}
}

func (f *fixture) credits(t *testing.T, id string) (ResetCredits, bool) {
	t.Helper()
	stored, ok := f.manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %s missing", id)
	}
	return ResetCreditsOf(stored)
}

func TestRefreshStaleLooksUpOnlyStaleCodexLogins(t *testing.T) {
	disabled := codexOAuth("disabled")
	disabled.Disabled = true
	apiKey := codexOAuth("api-key")
	apiKey.Attributes["api_key"] = "configured"
	claude := &coreauth.Auth{ID: "claude", Provider: "claude", Metadata: map[string]any{"access_token": "x"}}
	f := newFixture(t, codexOAuth("a"), disabled, apiKey, claude)

	f.tracker.RefreshStale(context.Background())
	if f.calls["a"] != 1 || len(f.calls) != 1 {
		t.Fatalf("first pass calls = %v", f.calls)
	}
	got, ok := f.credits(t, "a")
	if !ok || got.Available != 1 || got.Applicable != 0 || !got.CheckedAt.Equal(f.now) {
		t.Fatalf("credits = %+v, %v", got, ok)
	}

	f.now = f.now.Add(MaxAge - time.Minute)
	f.tracker.RefreshStale(context.Background())
	if f.calls["a"] != 1 {
		t.Fatalf("fresh lookup repeated: %v", f.calls)
	}
	f.now = f.now.Add(2 * time.Minute)
	f.tracker.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("stale lookup not refreshed: %v", f.calls)
	}
}

func TestRefreshStaleLooksUpOnBootDespiteASavedLookup(t *testing.T) {
	saved := codexOAuth("a")
	// A lookup saved by the previous run a minute before the fixture's clock; its windows were in memory and are gone.
	saved.Metadata[checkedAtKey] = "2026-10-26T11:59:00Z"
	f := newFixture(t, saved)
	f.tracker.RefreshStale(context.Background())
	if f.calls["a"] != 1 {
		t.Fatalf("boot pass skipped a credential with a saved lookup: %v", f.calls)
	}
}

func TestRefreshClearsCreditsTheAccountNoLongerReports(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	f.tracker.RefreshStale(context.Background())
	f.body = `{"plan_type":"plus"}`
	f.now = f.now.Add(MaxAge + time.Minute)
	f.tracker.RefreshStale(context.Background())
	if got, ok := f.credits(t, "a"); ok {
		t.Fatalf("credits kept after the account stopped reporting them: %+v", got)
	}
	f.now = f.now.Add(MaxAge - time.Minute)
	f.tracker.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("an empty report was not recorded as checked: %v", f.calls)
	}
}

func TestRefreshBacksOffAfterAFailure(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	f.failing = true
	f.tracker.RefreshStale(context.Background())
	f.now = f.now.Add(CheckInterval)
	f.tracker.RefreshStale(context.Background())
	if f.calls["a"] != 1 {
		t.Fatalf("retried inside the backoff: %v", f.calls)
	}
	f.failing = false
	f.now = f.now.Add(MaxAge)
	f.tracker.RefreshStale(context.Background())
	if _, ok := f.credits(t, "a"); !ok || f.calls["a"] != 2 {
		t.Fatalf("no recovery after the backoff: %v", f.calls)
	}
}

func TestRefreshRecordsUsageWindowsWithoutTraffic(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	f.body = `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,` +
		`"primary_window":{"used_percent":7,"limit_window_seconds":18000,"reset_after_seconds":10531,"reset_at":1793075116},` +
		`"secondary_window":{"used_percent":3,"limit_window_seconds":604800,"reset_after_seconds":546196,"reset_at":1793610781}},` +
		`"code_review_rate_limit":null}`
	if failures := f.tracker.RefreshAll(context.Background()); len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	stored, _ := f.manager.GetByID("a")
	windows := coreauth.QuotaWindows(stored, f.now)
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	want := []struct {
		percent  float64
		duration time.Duration
		resetsAt int64
	}{{7, 5 * time.Hour, 1793075116}, {3, 7 * 24 * time.Hour, 1793610781}}
	for i, w := range want {
		got := windows[i]
		if got.UsedPercent == nil || *got.UsedPercent != w.percent || got.Duration != w.duration || got.ResetsAt.Unix() != w.resetsAt {
			t.Fatalf("window %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestRefreshAllIgnoresStalenessAndSkipsIneligible(t *testing.T) {
	disabled := codexOAuth("disabled")
	disabled.Disabled = true
	f := newFixture(t, codexOAuth("a"), disabled)
	f.tracker.RefreshStale(context.Background())
	f.tracker.RefreshAll(context.Background())
	if f.calls["a"] != 2 || f.calls["disabled"] != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestResetCreditsOfReadsCountsAfterAJSONRoundTrip(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{availableKey: 2, applicableKey: 1, checkedAtKey: "2026-10-26T12:00:00Z"})
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	got, ok := ResetCreditsOf(&coreauth.Auth{Metadata: metadata})
	if !ok || got.Available != 2 || got.Applicable != 1 {
		t.Fatalf("credits = %+v, %v", got, ok)
	}
}

// creditList is a reset-credit list in ChatGPT's shape: two unspent credits out of order, plus ones the
// usage count leaves out (spent, another reset type, no expiry).
const creditList = `{"credits":[
	{"id":"c2","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-10-20T00:00:00Z","expires_at":"2026-12-01T08:30:00.123Z"},
	{"id":"c1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-10-01T00:00:00Z","expires_at":"2026-11-15T08:30:00Z"},
	{"id":"c3","reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-11-01T00:00:00Z"},
	{"id":"c4","reset_type":"other","status":"available","expires_at":"2026-11-02T00:00:00Z"},
	{"id":"c5","reset_type":"codex_rate_limits","status":"available"}]}`

func TestRefreshStoresUnspentCreditExpiriesSoonestFirst(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	f.body = `{"rate_limit_reset_credits":{"available_count":3,"applicable_available_count":1}}`
	f.creditsBody = creditList
	if failures := f.tracker.RefreshAll(context.Background()); len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	got, ok := f.credits(t, "a")
	want := []time.Time{time.Date(2026, 11, 15, 8, 30, 0, 0, time.UTC), time.Date(2026, 12, 1, 8, 30, 0, 0, time.UTC)}
	if !ok || got.Available != 3 || len(got.Expiries) != len(want) {
		t.Fatalf("credits = %+v, %v", got, ok)
	}
	for i, at := range want {
		if !got.Expiries[i].Equal(at) {
			t.Fatalf("expiry %d = %v, want %v", i, got.Expiries[i], at)
		}
	}
}

func TestRefreshSkipsTheCreditListWhenNoneAreHeld(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	f.body = `{"rate_limit_reset_credits":{"available_count":0,"applicable_available_count":0}}`
	f.tracker.RefreshAll(context.Background())
	if f.creditCalls["a"] != 0 {
		t.Fatalf("credit list read with no credits held: %v", f.creditCalls)
	}
}

func TestRefreshKeepsCountsWhenTheCreditListFails(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	f.creditsBody = creditList
	f.tracker.RefreshAll(context.Background())
	f.creditsFailing = true
	if failures := f.tracker.RefreshAll(context.Background()); len(failures) != 0 {
		t.Fatalf("a failed credit list failed the usage lookup: %v", failures)
	}
	got, ok := f.credits(t, "a")
	if !ok || got.Available != 1 || len(got.Expiries) != 0 {
		t.Fatalf("credits = %+v, %v; want counts kept and expiries unknown", got, ok)
	}
}

func TestResetCreditsOfReadsExpiriesAfterAJSONRoundTrip(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{availableKey: 1, applicableKey: 0, checkedAtKey: "2026-10-26T12:00:00Z", expiriesKey: []string{"2026-11-15T08:30:00Z"}})
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	got, ok := ResetCreditsOf(&coreauth.Auth{Metadata: metadata})
	if !ok || len(got.Expiries) != 1 || !got.Expiries[0].Equal(time.Date(2026, 11, 15, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("credits = %+v, %v", got, ok)
	}
}
