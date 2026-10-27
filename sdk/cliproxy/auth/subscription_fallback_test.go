package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSubscriptionFallbackReturnsFromAPIKeyAfterRecovery(t *testing.T) {
	for _, affinityEnabled := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			name := "single"
			if mixed {
				name = "mixed"
			}
			if affinityEnabled {
				name += "-affinity"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				provider := "subscription-fallback-" + name
				model := provider + "-model"
				manager := NewManager(nil, nil, nil)
				cfg := &config.Config{}
				cfg.Routing.PreferAuthKind = "oauth"
				manager.SetConfig(cfg)
				var selector Selector = &RoundRobinSelector{}
				if affinityEnabled {
					affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: selector, TTL: time.Hour})
					defer affinity.Stop()
					selector = affinity
				}
				manager.SetSelector(selector)
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				for _, auth := range []*Auth{
					{ID: provider + "-subscription", Provider: provider, Status: StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}},
					{ID: provider + "-key", Provider: provider, Status: StatusActive, Attributes: map[string]string{"api_key": "test-key", "priority": "20"}},
				} {
					if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				}
				opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "same-conversation"}}
				pick := func(options cliproxyexecutor.Options, tried map[string]struct{}) *Auth {
					t.Helper()
					var auth *Auth
					var err error
					if mixed {
						auth, _, _, err = manager.pickNextMixed(ctx, []string{provider}, model, options, tried)
					} else {
						auth, _, err = manager.pickNext(ctx, provider, model, options, tried)
					}
					if err != nil || auth == nil {
						t.Fatalf("pick = %v, %v", auth, err)
					}
					return auth
				}
				assertPick := func(want string) {
					t.Helper()
					if got := pick(opts, nil); got.ID != provider+want {
						t.Fatalf("picked %s, want %s", got.ID, provider+want)
					}
				}
				assertPick("-subscription")
				manager.MarkResult(ctx, Result{AuthID: provider + "-subscription", Provider: provider, Model: model,
					Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}})
				assertPick("-key")
				expireSessionAffinityPriorityModelCooldown(t, manager, provider+"-subscription", model)
				assertPick("-subscription")
				if got := pick(opts, map[string]struct{}{provider + "-subscription": {}}); got.ID != provider+"-key" {
					t.Fatal("an already-tried subscription prevented API-key fallback")
				}
				pinned := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: provider + "-key"}}
				if got := pick(pinned, nil); got.ID != provider+"-key" {
					t.Fatal("subscription preference overrode an explicit key pin")
				}
			})
		}
	}
}

func TestAPIKeyPreferenceFallsBackToSubscriptionAndReturns(t *testing.T) {
	ctx := context.Background()
	provider := "api-key-first"
	model := provider + "-model"
	manager := NewManager(nil, nil, nil)
	cfg := &config.Config{}
	cfg.Routing.PreferAuthKind = "api-key"
	manager.SetConfig(cfg)
	manager.SetSelector(&RoundRobinSelector{})
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	for _, auth := range []*Auth{
		{ID: provider + "-subscription", Provider: provider, Status: StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": "20"}},
		{ID: provider + "-key", Provider: provider, Status: StatusActive, Attributes: map[string]string{"api_key": "test-key"}},
	} {
		if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	assertPick := func(want string) {
		t.Helper()
		auth, _, err := manager.pickNext(ctx, provider, model, cliproxyexecutor.Options{}, nil)
		if err != nil || auth == nil || auth.ID != provider+want {
			t.Fatalf("picked %v (%v), want %s", auth, err, provider+want)
		}
	}
	assertPick("-key")
	manager.MarkResult(ctx, Result{AuthID: provider + "-key", Provider: provider, Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}})
	assertPick("-subscription")
	expireSessionAffinityPriorityModelCooldown(t, manager, provider+"-key", model)
	assertPick("-key")
}

func TestSubscriptionPreferenceIgnoresZeroWeightOAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := &config.Config{}
	cfg.Routing.PreferAuthKind = "oauth"
	manager.SetConfig(cfg)
	auths := []*Auth{
		{ID: "subscription", Provider: "test", Attributes: map[string]string{"auth_kind": "oauth", "weight": "0"}},
		{ID: "key", Provider: "test", Attributes: map[string]string{"api_key": "test-key"}},
	}
	_, candidates, err := manager.availableAuthsForSelector(&WeightedRoundRobinSelector{}, auths, "test", "", time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := (&WeightedRoundRobinSelector{}).Pick(context.Background(), "test", "", cliproxyexecutor.Options{}, candidates)
	if err != nil || selected == nil || selected.ID != "key" {
		t.Fatalf("zero-weight OAuth blocked API key: %v, %v", selected, err)
	}
}
