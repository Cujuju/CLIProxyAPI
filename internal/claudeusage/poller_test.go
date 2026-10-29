package claudeusage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type pollerFixture struct {
	poller  *Poller
	manager *coreauth.Manager
	now     time.Time
	calls   map[string]int
	failing map[string]bool
	// fiveHourIn sets the 5-hour reset returned by the fake lookup.
	fiveHourIn time.Duration
}

func newPollerFixture(t *testing.T, auths ...*coreauth.Auth) *pollerFixture {
	t.Helper()
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	f := &pollerFixture{manager: manager, now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), calls: map[string]int{}, failing: map[string]bool{}, fiveHourIn: 2 * time.Hour}
	f.poller = NewPoller(manager, func() *config.Config { return cfg })
	f.poller.spacing = 0
	f.poller.now = func() time.Time { return f.now }
	f.poller.fetch = func(_ context.Context, _ *config.Config, auth *coreauth.Auth) (json.RawMessage, error) {
		f.calls[auth.ID]++
		if f.failing[auth.ID] {
			return nil, errors.New("upstream unavailable")
		}
		return json.RawMessage(`{"five_hour":{"utilization":10,"resets_at":"` + f.now.Add(f.fiveHourIn).Format(time.RFC3339) + `"},` +
			`"seven_day":{"utilization":30,"resets_at":"` + f.now.Add(48*time.Hour).Format(time.RFC3339) + `"}}`), nil
	}
	return f
}

func claudeOAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "claude", Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "token-" + id}}
}

func TestPollerRefreshesOnlyStaleEligibleCredentials(t *testing.T) {
	disabled := claudeOAuth("disabled")
	disabled.Disabled = true
	apiKey := claudeOAuth("api-key")
	apiKey.Attributes["api_key"] = "configured"
	f := newPollerFixture(t, claudeOAuth("a"), disabled, apiKey, &coreauth.Auth{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "x"}})

	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 1 || len(f.calls) != 1 {
		t.Fatalf("first pass calls = %v", f.calls)
	}
	stored, _ := f.manager.GetByID("a")
	windows := coreauth.QuotaWindows(stored, f.now)
	if len(windows) != 2 || !windows[1].ResetsAt.Equal(f.now.Add(48*time.Hour)) || !stored.Quota.LookedUpAt.Equal(f.now) {
		t.Fatalf("snapshot = %+v windows = %+v", stored.Quota, windows)
	}

	f.now = f.now.Add(MaxLookupAge - time.Minute)
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 1 {
		t.Fatalf("fresh snapshot was looked up again: %v", f.calls)
	}

	f.now = f.now.Add(2 * time.Minute)
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("old snapshot was not refreshed: %v", f.calls)
	}
}

func TestPollerRefreshesWhenAccountWindowResets(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	f.fiveHourIn = MaxLookupAge / 3
	f.poller.RefreshStale(context.Background())
	// Past the 5-hour reset while the lookup is still younger than MaxLookupAge.
	f.now = f.now.Add(MaxLookupAge / 2)
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestPollerBacksOffFailingCredential(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	f.failing["a"] = true
	f.poller.RefreshStale(context.Background())
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 1 {
		t.Fatalf("failing credential retried inside backoff: %v", f.calls)
	}
	f.now = f.now.Add(FailureBackoff + time.Second)
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("credential not retried after backoff: %v", f.calls)
	}
	if failures := f.poller.RefreshAll(context.Background()); failures["a"] == nil || f.calls["a"] != 3 {
		t.Fatalf("RefreshAll should ignore backoff and report failures: %v %v", failures, f.calls)
	}
}

func TestObserveQuotaLookupKeepsNewerSnapshot(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	f.poller.RefreshStale(context.Background())
	older := f.now.Add(-time.Minute)
	if f.manager.ObserveQuotaLookup("a", map[string][]string{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.9"}}, older) {
		t.Fatal("older lookup replaced a newer one")
	}
}

func TestPollerWaitsForExpiredTokenWithoutBackoff(t *testing.T) {
	auth := claudeOAuth("a")
	f := newPollerFixture(t, auth)
	stored, _ := f.manager.GetByID("a")
	stored.Metadata["expired"] = f.now.Add(-time.Minute).Format(time.RFC3339)
	if _, errUpdate := f.manager.Update(context.Background(), stored); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if !f.poller.RefreshStale(context.Background()) || f.calls["a"] != 0 {
		t.Fatalf("expired token should defer the lookup: calls = %v", f.calls)
	}
	stored, _ = f.manager.GetByID("a")
	stored.Metadata["expired"] = f.now.Add(time.Hour).Format(time.RFC3339)
	if _, errUpdate := f.manager.Update(context.Background(), stored); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if f.poller.RefreshStale(context.Background()) || f.calls["a"] != 1 {
		t.Fatalf("renewed token should be looked up at once: calls = %v", f.calls)
	}
}

func TestPollerRefreshesWhenScopedWindowResets(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	reset := f.now.Add(MaxLookupAge / 3)
	f.poller.fetch = func(_ context.Context, _ *config.Config, auth *coreauth.Auth) (json.RawMessage, error) {
		f.calls[auth.ID]++
		return json.RawMessage(`{"seven_day_sonnet":{"utilization":50,"resets_at":"` + reset.Format(time.RFC3339) + `"}}`), nil
	}
	f.poller.RefreshStale(context.Background())
	f.now = f.now.Add(MaxLookupAge / 2)
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("scoped reset should force a lookup: calls = %v", f.calls)
	}
	// The second lookup still reports the passed reset; that must not loop.
	f.poller.RefreshStale(context.Background())
	if f.calls["a"] != 2 {
		t.Fatalf("reset reported by the lookup itself forced another: calls = %v", f.calls)
	}
}

func TestLookupWindowsSurviveResponseHeaders(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	f.poller.fetch = func(_ context.Context, _ *config.Config, auth *coreauth.Auth) (json.RawMessage, error) {
		return json.RawMessage(`{"five_hour":{"utilization":10},"seven_day_sonnet":{"utilization":50},` +
			`"monthly_spend":{"utilization":20,"limit_dollars":100}}`), nil
	}
	notified := 0
	stop := f.manager.SubscribeQuotaUpdates(func() { notified++ })
	defer stop()
	f.poller.RefreshStale(context.Background())
	if notified != 1 {
		t.Fatalf("lookup subscribers notified %d times", notified)
	}

	stored, _ := f.manager.GetByID("a")
	stored.Quota.ObserveResponseHeadersForProvider("claude", map[string][]string{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.4"}}, f.now.Add(time.Minute))
	got := map[string]float64{}
	labels := map[string]string{}
	for _, window := range coreauth.QuotaWindows(stored, f.now) {
		got[window.Key] = *window.UsedPercent
		labels[window.Key] = window.Label
	}
	if len(got) != 3 || got["five_hour"] != 40 || got["seven_day_sonnet"] != 50 || got["monthly_spend"] != 20 {
		t.Fatalf("windows after traffic = %v", got)
	}
	if labels["monthly_spend"] != "Spend limit $100" {
		t.Fatalf("lookup label lost: %v", labels)
	}
}
