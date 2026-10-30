package auth

import (
	"strings"
	"testing"
	"time"
)

func TestLookupDriftLinePairsLookupWithHeaderReading(t *testing.T) {
	lookedUpAt := time.Now()
	a := &Auth{ID: "claude-a.json", Provider: "claude", Index: "abc123"}
	a.Quota.Signals = map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.57"}
	a.Quota.ObservedAt = lookedUpAt.Add(-30 * time.Second)
	lookup := map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization":        "0.58",
		"Anthropic-Ratelimit-Unified-7d_sonnet-Utilization": "0.2",
	}
	line := lookupDriftLine(a, lookup, lookedUpAt)
	for _, want := range []string{"auth_index=abc123", "header_age=30s", "five_hour=58/57", "seven_day_sonnet=20/-"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q missing %q", line, want)
		}
	}
}
