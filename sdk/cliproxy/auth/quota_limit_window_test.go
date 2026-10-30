package auth

import "testing"

func TestClaudeQuotaWindowsExactDecimalPercent(t *testing.T) {
	for _, tc := range []struct {
		fraction string
		percent  float64
	}{{"0.57", 57}, {"0.55", 55}, {"0.4237", 42.37}} {
		windows := claudeQuotaWindows(map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Utilization": tc.fraction,
		})
		if len(windows) != 1 || windows[0].UsedPercent == nil {
			t.Fatalf("%s: missing utilization", tc.fraction)
		}
		if got := *windows[0].UsedPercent; got != tc.percent {
			t.Errorf("%s: UsedPercent = %.17g, want %.17g", tc.fraction, got, tc.percent)
		}
	}
}
