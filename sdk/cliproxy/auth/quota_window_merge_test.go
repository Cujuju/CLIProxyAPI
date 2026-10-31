package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestQuotaWindowsMergeClaude(t *testing.T) {
	now := time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	for _, tc := range []struct {
		name          string
		responseUsage string
		lookupUsage   string
		responseReset time.Time
		lookupReset   time.Time
		lookupNewer   bool
		wantPercent   float64
		wantStatus    string
	}{
		{"newer header lower same period", "0.8", "0.81", reset.Add(time.Second), reset, false, 81, "allowed_warning"},
		{"newer header higher", "0.82", "0.81", reset, reset, false, 82, "allowed_warning"},
		{"newer lookup lower early reset", "0.81", "0.2", reset, reset, true, 20, "allowed"},
		{"newer header lower different period", "0.8", "0.81", reset.Add(2 * time.Second), reset, false, 80, "allowed_warning"},
		{"newer header lower lookup missing reset", "0.8", "0.81", reset, time.Time{}, false, 80, "allowed_warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": tc.responseUsage,
				"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(tc.responseReset.Unix(), 10),
				"Anthropic-Ratelimit-Unified-5h-Status":      "allowed_warning",
			}
			lookup := map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": tc.lookupUsage,
				"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
			}
			if !tc.lookupReset.IsZero() {
				lookup["Anthropic-Ratelimit-Unified-5h-Reset"] = strconv.FormatInt(tc.lookupReset.Unix(), 10)
			}
			auth := &Auth{Provider: "claude"}
			auth.Quota.Signals = response
			auth.Quota.LookupSignals = lookup
			auth.Quota.ObservedAt = now
			auth.Quota.LookedUpAt = now.Add(-time.Second)
			wantReset := tc.responseReset
			if tc.lookupNewer {
				auth.Quota.LookedUpAt = now.Add(time.Second)
				wantReset = tc.lookupReset
			}
			windows := QuotaWindows(auth, now)
			if len(windows) != 1 {
				t.Fatalf("got %d windows, want 1", len(windows))
			}
			window := windows[0]
			if window.UsedPercent == nil {
				t.Fatal("missing UsedPercent")
			}
			if got := *window.UsedPercent; got != tc.wantPercent {
				t.Errorf("UsedPercent = %v, want %v", got, tc.wantPercent)
			}
			if window.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", window.Status, tc.wantStatus)
			}
			if !window.ResetsAt.Equal(wantReset) {
				t.Errorf("ResetsAt = %v, want %v", window.ResetsAt, wantReset)
			}
		})
	}
}
