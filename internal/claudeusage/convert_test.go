package claudeusage

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestWindowsFromUsageExactDecimalPercent(t *testing.T) {
	for _, percent := range []float64{57, 55, 42.37, 0.7} {
		windows, err := WindowsFromUsage([]byte(fmt.Sprintf(`{"seven_day":{"utilization":%s}}`, strconv.FormatFloat(percent, 'f', -1, 64))))
		if err != nil {
			t.Fatal(err)
		}
		if len(windows) != 1 || windows[0].UsedPercent == nil {
			t.Fatal("missing utilization")
		}
		if got := *windows[0].UsedPercent; got != percent {
			t.Errorf("lookup %.17g: UsedPercent = %.17g", percent, got)
		}
	}
}

// usageDocument mirrors a live /api/oauth/usage response (values changed).
const usageDocument = `{
  "five_hour": {"utilization": 12.0, "resets_at": "2026-10-25T18:00:00.123456+00:00"},
  "seven_day": {"utilization": 41.0, "resets_at": "2026-10-29T09:00:00+00:00"},
  "seven_day_oauth_apps": null,
  "iguana_necktie": {"utilization": 19.99, "resets_at": "2026-11-05T00:00:00+00:00", "limit_dollars": 250},
  "extra_usage": {"is_enabled": false, "utilization": null},
  "limits": [
    {"kind": "session", "percent": 12, "severity": "normal", "resets_at": "2026-10-25T18:00:00.123456+00:00"},
    {"kind": "weekly_all", "percent": 41, "severity": "warning", "resets_at": "2026-10-29T09:00:00+00:00"},
    {"kind": "weekly_scoped", "percent": 100, "severity": "critical", "resets_at": "2026-10-30T09:00:00+00:00", "scope": {"model": {"display_name": "Fable"}}}
  ]
}`

func TestWindowsFromUsage(t *testing.T) {
	windows, err := WindowsFromUsage([]byte(usageDocument))
	if err != nil {
		t.Fatal(err)
	}
	legacy := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization":             {"0.12"},
		"Anthropic-Ratelimit-Unified-5h-Status":                  {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":             {"0.41"},
		"Anthropic-Ratelimit-Unified-7d-Status":                  {"allowed_warning"},
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization":          {"1"},
		"Anthropic-Ratelimit-Unified-7d_oi-Status":               {"rejected"},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":                {unix("2026-10-30T09:00:00Z")},
		"Anthropic-Ratelimit-Unified-Iguana_necktie-Utilization": {"0.1999"},
		"Anthropic-Ratelimit-Unified-Iguana_necktie-Label":       {"Spend limit $250"},
		"Anthropic-Ratelimit-Unified-Iguana_necktie-Reset":       {unix("2026-11-05T00:00:00Z")},
		"Anthropic-Ratelimit-Unified-7d-Reset":                   {unix("2026-10-29T09:00:00Z")},
		"Anthropic-Ratelimit-Unified-5h-Reset":                   {unix("2026-10-25T18:00:00Z")},
	}
	now := time.Now()
	a := &coreauth.Auth{Provider: "claude"}
	a.Quota.ObserveLookupForProvider("claude", legacy, now)
	want := coreauth.QuotaWindows(a, now)
	a.Quota.LookupSignals = nil
	a.Quota.LookupWindows = windows
	if got := coreauth.QuotaWindows(a, now); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized windows = %+v, want %+v", got, want)
	}
}

func TestWindowsFromUsageWithoutLimitsArray(t *testing.T) {
	windows, err := WindowsFromUsage([]byte(`{"five_hour": {"utilization": 50, "resets_at": "2026-10-25T18:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 || windows[0].Key != "five_hour" || windows[0].Status != "" ||
		windows[0].UsedPercent == nil || *windows[0].UsedPercent != 50 ||
		strconv.FormatInt(windows[0].ResetsAt.Unix(), 10) != unix("2026-10-25T18:00:00Z") {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestWindowsFromUsageRejectsEmpty(t *testing.T) {
	if _, err := WindowsFromUsage([]byte(`{"extra_usage": {"utilization": 3}}`)); err == nil {
		t.Fatal("expected error for a document without usage windows")
	}
	if _, err := WindowsFromUsage([]byte(`not json`)); err == nil {
		t.Fatal("expected parse error")
	}
}

func unix(value string) string {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return strconv.FormatInt(at.Unix(), 10)
}
