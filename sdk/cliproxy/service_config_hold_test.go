package cliproxy

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRoutingStateParsesSessionAffinityHold(t *testing.T) {
	tests := []struct {
		affinity bool
		hold     string
		want     time.Duration
	}{
		{true, "45m", 45 * time.Minute},
		{true, "", 0},
		{true, "0", 0},
		{true, "soon", 0},
		{false, "45m", 0},
	}
	for _, tc := range tests {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{SessionAffinity: tc.affinity, SessionAffinityHold: tc.hold},
		})
		if state.sessionAffinityHold != tc.want {
			t.Errorf("affinity %v hold %q: got %v, want %v", tc.affinity, tc.hold, state.sessionAffinityHold, tc.want)
		}
	}
}

func TestApplyManagerConfigReplacesSelectorWhenHoldChanges(t *testing.T) {
	service := &Service{coreManager: coreauth.NewManager(nil, nil, nil)}
	apply := func(hold string) coreauth.Selector {
		t.Helper()
		cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{
			SessionAffinity: true, SessionAffinityTTL: "1h", SessionAffinityHold: hold,
		}}
		if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg}) {
			t.Fatal("applyManagerConfig failed")
		}
		selector := service.coreManager.Selector()
		if _, ok := selector.(*coreauth.SessionAffinitySelector); !ok {
			t.Fatalf("selector = %T, want session affinity", selector)
		}
		return selector
	}
	before := apply("0")
	if unchanged := apply("0"); unchanged != before {
		t.Fatal("unchanged hold replaced the selector")
	}
	after := apply("30m")
	if after == before {
		t.Fatal("enabling hold reused the disabled selector")
	}
	t.Cleanup(after.(*coreauth.SessionAffinitySelector).Stop)
}
