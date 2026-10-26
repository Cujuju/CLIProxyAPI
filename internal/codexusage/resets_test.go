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
	f := &fixture{manager: manager, now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), calls: map[string]int{},
		body: `{"plan_type":"plus","rate_limit_reset_credits":{"available_count":1,"applicable_available_count":0}}`}
	f.tracker = NewTracker(manager, func() *config.Config { return cfg })
	f.tracker.spacing = 0
	f.tracker.now = func() time.Time { return f.now }
	f.tracker.fetch = func(_ context.Context, _ *config.Config, auth *coreauth.Auth) ([]byte, error) {
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
