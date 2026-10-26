package claudeusage

import (
	"context"
	"errors"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPlanOfReadsTierMultiple(t *testing.T) {
	for _, tc := range []struct {
		tier, planType, label string
		multiplier            float64
	}{
		{"default_claude_max_5x", "claude_max", "Max 5x", 5},
		{"default_claude_max_20x", "claude_max", "Max 20x", 20},
		{"default_claude_ai", "claude_pro", "Pro", 1},
		{"", "claude_team", "Team", 0},
		{"default_claude_custom", "", "default_claude_custom", 0},
	} {
		auth := &coreauth.Auth{Metadata: map[string]any{planTierKey: tc.tier, planTypeKey: tc.planType}}
		plan, ok := PlanOf(auth)
		if !ok || plan.Label != tc.label || plan.Multiplier != tc.multiplier {
			t.Fatalf("%s/%s: plan = %+v, ok = %v", tc.tier, tc.planType, plan, ok)
		}
	}
	if _, ok := PlanOf(&coreauth.Auth{}); ok {
		t.Fatal("plan before any lookup")
	}
}

func TestPlanTrackerStoresPlanAndWaitsForMaxAge(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	tracker := NewPlanTracker(f.manager, func() *config.Config { return &config.Config{} })
	tracker.spacing = 0
	tracker.now = func() time.Time { return f.now }
	calls := 0
	tracker.fetch = func(context.Context, *config.Config, *coreauth.Auth) (*claudeauth.OAuthProfile, error) {
		calls++
		profile := &claudeauth.OAuthProfile{}
		profile.Organization.RateLimitTier = "default_claude_max_5x"
		profile.Organization.OrganizationType = "claude_max"
		return profile, nil
	}

	tracker.RefreshStale(context.Background())
	auth, _ := f.manager.GetByID("a")
	if plan, ok := PlanOf(auth); !ok || plan.Multiplier != 5 {
		t.Fatalf("plan = %+v, ok = %v", plan, ok)
	}
	tracker.RefreshStale(context.Background())
	f.now = f.now.Add(PlanMaxAge + time.Minute)
	tracker.RefreshStale(context.Background())
	if calls != 2 {
		t.Fatalf("calls = %d, want a lookup now and one after PlanMaxAge", calls)
	}
}

func TestPlanTrackerBacksOffAfterFailure(t *testing.T) {
	f := newPollerFixture(t, claudeOAuth("a"))
	tracker := NewPlanTracker(f.manager, func() *config.Config { return &config.Config{} })
	tracker.spacing = 0
	tracker.now = func() time.Time { return f.now }
	calls := 0
	tracker.fetch = func(context.Context, *config.Config, *coreauth.Auth) (*claudeauth.OAuthProfile, error) {
		calls++
		return nil, errors.New("403 insufficient_scope")
	}
	tracker.RefreshStale(context.Background())
	f.now = f.now.Add(PlanCheckInterval)
	tracker.RefreshStale(context.Background())
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 inside the backoff", calls)
	}
}
