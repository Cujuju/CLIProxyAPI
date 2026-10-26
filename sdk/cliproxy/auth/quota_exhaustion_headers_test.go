package auth

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// usageLimitClientHeaders returns what a client receives for a usage-limit 429.
func usageLimitClientHeaders(limit quotaLimit) http.Header {
	err := newModelCooldownError("m", "claude", time.Hour)
	err.usageLimit = limit
	dst := http.Header{}
	CopySafeErrorHeaders(dst, err)
	return dst
}

func TestUsageLimitClientHeaders_NameTheSpentWindow(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		limit        quotaLimit
		claim, token string
		codexMinutes string
	}{
		{"five-hour window", quotaLimit{ResetsAt: now.Add(2 * time.Hour), Window: fiveHourWindow}, claudeFiveHourClaim, claudeFiveHourToken, "300"},
		{"weekly window resetting soon", quotaLimit{ResetsAt: now.Add(time.Hour), Window: weeklyWindow}, claudeWeeklyClaim, claudeWeeklyToken, "10080"},
		{"unknown window, near reset", quotaLimit{ResetsAt: now.Add(2 * time.Hour)}, claudeFiveHourClaim, claudeFiveHourToken, ""},
		{"unknown window, far reset", quotaLimit{ResetsAt: now.Add(72 * time.Hour)}, claudeWeeklyClaim, claudeWeeklyToken, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := usageLimitClientHeaders(tc.limit)
			reset := strconv.FormatInt(tc.limit.ResetsAt.Unix(), 10)
			want := map[string]string{
				"Anthropic-Ratelimit-Unified-Status":                       "rejected",
				"Anthropic-Ratelimit-Unified-Reset":                        reset,
				"Anthropic-Ratelimit-Unified-Representative-Claim":         tc.claim,
				"Anthropic-Ratelimit-Unified-" + tc.token + "-Status":      "rejected",
				"Anthropic-Ratelimit-Unified-" + tc.token + "-Reset":       reset,
				"Anthropic-Ratelimit-Unified-" + tc.token + "-Utilization": claudeSpentUtilization,
				"X-Codex-Primary-Used-Percent":                             "100",
				"X-Codex-Primary-Reset-At":                                 reset,
				"X-Codex-Primary-Window-Minutes":                           tc.codexMinutes,
				HeaderShouldRetry:                                          "false",
				"Retry-After":                                              "",
			}
			for name, value := range want {
				if got := headers.Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}

func TestUsageLimitClientHeaders_OrdinaryCooldownHasNone(t *testing.T) {
	dst := http.Header{}
	CopySafeErrorHeaders(dst, newModelCooldownError("m", "claude", time.Minute))
	for name := range dst {
		if isUsageLimitHeader(name) {
			t.Fatalf("retryable cooldown leaked usage-limit header %s", name)
		}
	}
}

// The proxy's own quota parser must read the headers back as the same spent window.
func TestUsageLimitClientHeaders_RoundTripThroughQuotaParser(t *testing.T) {
	now := time.Now()
	limit := quotaLimit{ResetsAt: now.Add(3 * time.Hour).Truncate(time.Second), Window: fiveHourWindow}
	headers := usageLimitClientHeaders(limit)
	for _, provider := range []string{"claude", "codex"} {
		signals := map[string]string{}
		for name := range headers {
			signals[name] = headers.Get(name)
		}
		auth := &Auth{ID: "rt-" + provider, Provider: provider, Quota: QuotaState{Signals: signals, ObservedAt: now}}
		got, ok := credentialQuotaLimit(auth, "m", now)
		if !ok || !got.ResetsAt.Equal(limit.ResetsAt) || got.Window != limit.Window {
			t.Errorf("%s: parsed %+v %v, want %+v", provider, got, ok, limit)
		}
	}
}
