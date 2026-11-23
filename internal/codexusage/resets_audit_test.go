package codexusage

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexRefreshStampsBeforeFetchAndHonorsLimitReached(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	auth, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-1",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tracker := NewTracker(manager, func() *config.Config { return &config.Config{} })
	tracker.now = func() time.Time { return now }
	tracker.fetch = func(_ context.Context, _ *config.Config, _ *coreauth.Auth, _ string) ([]byte, error) {
		// Advance clock during fetch so a post-fetch timestamp would be newer than T+10s.
		now = now.Add(30 * time.Second)
		return []byte(`{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":40,"limit_window_seconds":18000,"reset_at":1795470000}}}`), nil
	}

	if err := tracker.Refresh(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	stored, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth missing")
	}
	wantStamped := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if !stored.Quota.LookedUpAt.Equal(wantStamped) {
		t.Fatalf("LookedUpAt = %v, want pre-fetch %v", stored.Quota.LookedUpAt, wantStamped)
	}
	if len(stored.Quota.LookupWindows) != 1 || stored.Quota.LookupWindows[0].Status != coreauth.QuotaWindowStatusRejected {
		t.Fatalf("expected limit_reached window to have rejected status, got %+v", stored.Quota.LookupWindows)
	}
}
