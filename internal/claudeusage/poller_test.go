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
	if len(windows) != 2 || !windows[1].ResetsAt.Equal(f.now.Add(48*time.Hour)) || !stored.Quota.ObservedAt.Equal(f.now) {
		t.Fatalf("snapshot = %+v windows = %+v", stored.Quota, windows)
	}

	f.now = f.now.Add(MaxObservationAge - time.Minute)
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
	f.fiveHourIn = MaxObservationAge / 3
	f.poller.RefreshStale(context.Background())
	// Past the 5-hour reset while the snapshot is still younger than MaxObservationAge.
	f.now = f.now.Add(MaxObservationAge / 2)
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

func TestObserveQuotaHeadersKeepsNewerSnapshot(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	f.poller.RefreshStale(context.Background())
	older := f.now.Add(-time.Minute)
	if f.manager.ObserveQuotaHeaders("a", map[string][]string{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.9"}}, older) {
		t.Fatal("older observation replaced a newer one")
	}
}
