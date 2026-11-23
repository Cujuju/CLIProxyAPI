package claudeusage

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResponseAndLookupWindowsUseSamePercentScale(t *testing.T) {
	lookup, err := WindowsFromUsage([]byte(`{"five_hour":{"utilization":42.37},"seven_day":{"utilization":19.99},"iguana_necktie":{"utilization":12.5,"limit_dollars":250}}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := &coreauth.Auth{Provider: "claude"}
	a.Quota.LookupWindows = lookup
	a.Quota.LookedUpAt = now
	a.Quota.ObserveResponseHeadersForProvider("claude", http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.4237"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.1999"},
	}, now.Add(time.Second))
	assertWindows := func() {
		t.Helper()
		windows := coreauth.QuotaWindows(a.Clone(), now)
		want := map[string]float64{"five_hour": 42.37, "seven_day": 19.99, "iguana_necktie": 12.5}
		if len(windows) != len(want) {
			t.Fatalf("windows = %+v", windows)
		}
		for _, w := range windows {
			if w.UsedPercent == nil || *w.UsedPercent != want[w.Key] {
				t.Fatalf("%s utilization = %v", w.Key, w.UsedPercent)
			}
		}
	}
	assertWindows()
	// A newer lookup of the same usage must not change any displayed percentage.
	a.Quota.LookedUpAt = now.Add(2 * time.Second)
	assertWindows()
}
