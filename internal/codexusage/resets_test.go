package codexusage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
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

func (f *fixture) credits(t *testing.T, id string) (resetcredits.Credits, bool) {
	t.Helper()
	stored, ok := f.manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %s missing", id)
	}
	return resetcredits.Of(stored)
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
	resetcredits.Store(saved.Metadata, time.Date(2026, 10, 26, 11, 59, 0, 0, time.UTC), nil)
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
	if len(stored.Quota.LookupSignals) != 0 || len(stored.Quota.LookupWindows) != 2 {
		t.Fatalf("expected normalized lookup, got %+v", stored.Quota)
	}
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

func TestUsageDocumentWindowParity(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	reset := f.now.Add(5*time.Hour - time.Minute).Unix()
	f.body = fmt.Sprintf(`{
		"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18001,"reset_at":%d}},
		"code_review_rate_limit":{
			"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_at":%d},
			"secondary_window":{"limit_window_seconds":18000,"reset_at":%d}}
	}`, reset, reset, reset)
	if failures := f.tracker.RefreshAll(t.Context()); len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	auth, _ := f.manager.GetByID("a")
	got := coreauth.QuotaWindows(auth, f.now)
	legacy := &coreauth.Auth{Provider: "codex"}
	rawReset := strconv.FormatInt(reset, 10)
	legacy.Quota.ObserveLookupForProvider("codex", http.Header{
		"X-Codex-Primary-Used-Percent":               {"0"},
		"X-Codex-Primary-Window-Minutes":             {"300"},
		"X-Codex-Primary-Reset-At":                   {rawReset},
		"X-Codex-Code-Review-Primary-Used-Percent":   {"0"},
		"X-Codex-Code-Review-Primary-Window-Minutes": {"300"},
		"X-Codex-Code-Review-Primary-Reset-At":       {rawReset},
	}, f.now)
	want := coreauth.QuotaWindows(legacy, f.now)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized windows = %+v, want %+v", got, want)
	}
	if len(got) != 2 || got[0].Cadence != coreauth.ResetCadenceOnUse || !got[0].ResetsAt.IsZero() ||
		!got[1].Scoped || got[1].ResetsAt.Unix() != reset || got[1].Label != "code-review · 5-hour" {
		t.Fatalf("unexpected unstarted or scoped window: %+v", got)
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

func TestRefreshFlagsOnlyReplacesPriorSnapshotWithoutLiftingCooldown(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	reset := f.now.Add(time.Hour).Unix()
	f.body = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":%d}}}`, reset)
	if failures := f.tracker.RefreshAll(t.Context()); len(failures) != 0 {
		t.Fatalf("initial refresh failures = %v", failures)
	}
	stored, _ := f.manager.GetByID("a")
	retry := time.Hour
	f.manager.MarkResult(t.Context(), coreauth.Result{
		AuthID: stored.ID, Provider: "codex", CredentialScope: true, RetryAfter: &retry,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"usage_limit_reached"}}`},
	})
	f.now = f.now.Add(time.Minute)
	f.body = `{"rate_limit":{"allowed":true,"limit_reached":false}}`
	if failures := f.tracker.RefreshAll(t.Context()); len(failures) != 0 {
		t.Fatalf("flags-only refresh failures = %v", failures)
	}
	after, _ := f.manager.GetByID("a")
	if len(coreauth.QuotaWindows(after, f.now)) != 0 || !after.Quota.LookedUpAt.Equal(f.now) {
		t.Fatalf("flags-only response left stale snapshot: %+v", after.Quota)
	}
	if !after.Quota.Exceeded || !after.Unavailable {
		t.Fatalf("flags-only response lifted cooldown: %+v", after)
	}
}

func TestRefreshRejectsReplacementCredentialAndOlderSnapshot(t *testing.T) {
	f := newFixture(t, codexOAuth("a"))
	orig, _ := f.manager.GetByID("a")
	f.body = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":40,"limit_window_seconds":18000,"reset_at":%d}}}`, f.now.Add(time.Hour).Unix())
	fetch := f.tracker.fetch
	f.tracker.fetch = func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, url string) ([]byte, error) {
		raw, err := fetch(ctx, cfg, auth, url)
		if err != nil {
			return nil, err
		}
		f.manager.Remove(t.Context(), auth.ID)
		if _, errReg := f.manager.Register(t.Context(), &coreauth.Auth{ID: auth.ID, Provider: "claude", Metadata: map[string]any{"access_token": "c"}}); errReg != nil {
			t.Fatal(errReg)
		}
		return raw, nil
	}
	if err := f.tracker.Refresh(t.Context(), orig); !errors.Is(err, coreauth.ErrQuotaLookupCredentialChanged) {
		t.Fatalf("replacement err = %v, want ErrQuotaLookupCredentialChanged", err)
	}
	replaced, _ := f.manager.GetByID("a")
	if len(replaced.Quota.LookupWindows) != 0 {
		t.Fatalf("codex windows attached to claude credential: %+v", replaced.Quota.LookupWindows)
	}

	// Older lookup must not overwrite a newer snapshot.
	f2 := newFixture(t, codexOAuth("b"))
	f2.body = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_at":%d}}}`, f2.now.Add(time.Hour).Unix())
	if failures := f2.tracker.RefreshAll(t.Context()); len(failures) != 0 {
		t.Fatalf("initial failures = %v", failures)
	}
	f2.now = f2.now.Add(-time.Minute)
	f2.body = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":90,"limit_window_seconds":18000,"reset_at":%d}}}`, f2.now.Add(time.Hour).Unix())
	if failures := f2.tracker.RefreshAll(t.Context()); len(failures) != 0 {
		t.Fatalf("older refresh failures = %v", failures)
	}
	after, _ := f2.manager.GetByID("b")
	if windows := coreauth.QuotaWindows(after, f2.now); len(windows) != 1 || *windows[0].UsedPercent != 20 {
		t.Fatalf("older lookup overwrote newer snapshot: %+v", windows)
	}
}
