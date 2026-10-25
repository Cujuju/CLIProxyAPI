package claudeusage

import (
	"strconv"
	"testing"
	"time"
)

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

func TestHeadersFromUsage(t *testing.T) {
	headers, err := HeadersFromUsage([]byte(usageDocument))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization":             "0.12",
		"Anthropic-Ratelimit-Unified-5h-Status":                  "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":             "0.41",
		"Anthropic-Ratelimit-Unified-7d-Status":                  "allowed_warning",
		"Anthropic-Ratelimit-Unified-7d_oi-Status":               "rejected",
		"Anthropic-Ratelimit-Unified-Iguana_necktie-Utilization": "0.1999",
		"Anthropic-Ratelimit-Unified-Iguana_necktie-Label":       "Spend limit $250",
		"Anthropic-Ratelimit-Unified-7d-Reset":                   unix("2026-10-29T09:00:00Z"),
		"Anthropic-Ratelimit-Unified-5h-Reset":                   unix("2026-10-25T18:00:00Z"),
	}
	for key, value := range want {
		if got := headers.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	for _, key := range []string{"Anthropic-Ratelimit-Unified-Extra_usage-Utilization", "Anthropic-Ratelimit-Unified-Seven_day_oauth_apps-Utilization"} {
		if headers.Get(key) != "" {
			t.Errorf("%s should be absent", key)
		}
	}
}

func TestHeadersFromUsageWithoutLimitsArray(t *testing.T) {
	headers, err := HeadersFromUsage([]byte(`{"five_hour": {"utilization": 50, "resets_at": "2026-10-25T18:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("Anthropic-Ratelimit-Unified-5h-Utilization") != "0.5" || headers.Get("Anthropic-Ratelimit-Unified-5h-Reset") != unix("2026-10-25T18:00:00Z") {
		t.Fatalf("headers = %v", headers)
	}
}

func TestHeadersFromUsageRejectsEmpty(t *testing.T) {
	if _, err := HeadersFromUsage([]byte(`{"extra_usage": {"utilization": 3}}`)); err == nil {
		t.Fatal("expected error for a document without usage windows")
	}
	if _, err := HeadersFromUsage([]byte(`not json`)); err == nil {
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
