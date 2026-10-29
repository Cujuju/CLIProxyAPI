package cliproxy

import (
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
