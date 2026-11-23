package auth

import (
	"maps"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestQuotaLookupWindowRoutingParity(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	reset := now.Add(time.Hour)
	resetRaw := strconv.FormatInt(reset.Unix(), 10)
	for _, tc := range []struct {
		provider string
		account  map[string]string
		scoped   map[string]string
	}{
		{"claude", map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": "1",
			"Anthropic-Ratelimit-Unified-5h-Reset":       resetRaw,
		}, map[string]string{
			"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "1",
			"Anthropic-Ratelimit-Unified-7d_oi-Reset":       strconv.FormatInt(reset.Add(time.Hour).Unix(), 10),
		}},
		{"codex", map[string]string{
			"X-Codex-Primary-Used-Percent":   "100",
			"X-Codex-Primary-Window-Minutes": "300",
			"X-Codex-Primary-Reset-At":       resetRaw,
		}, map[string]string{
			"X-Codex-Code-Review-Primary-Used-Percent":   "100",
			"X-Codex-Code-Review-Primary-Window-Minutes": "300",
			"X-Codex-Code-Review-Primary-Reset-At":       strconv.FormatInt(reset.Add(time.Hour).Unix(), 10),
		}},
	} {
		for _, scenario := range []struct {
			name       string
			observedAt time.Time
		}{
			{"standalone", time.Time{}},
			{"lookup newer", now.Add(-time.Minute)},
			{"response newer", now.Add(time.Minute)},
		} {
			t.Run(tc.provider+"/"+scenario.name, func(t *testing.T) {
				lookup := maps.Clone(tc.account)
				maps.Copy(lookup, tc.scoped)
				legacy := &Auth{Provider: tc.provider, Quota: QuotaState{LookedUpAt: now, LookupSignals: lookup}}
				if !scenario.observedAt.IsZero() {
					legacy.Quota.Signals = maps.Clone(tc.account)
					maps.Copy(legacy.Quota.Signals, collectQuotaSignals(tc.provider, recoveryLookupHeaders(tc.provider, false)))
					legacy.Quota.ObservedAt = scenario.observedAt
				}
				normalized := legacy.Clone()
				normalized.Quota.LookupWindows = signalWindows(tc.provider, lookup, now)
				normalized.Quota.LookupSignals = nil

				wantWindows := QuotaWindows(legacy, now)
				if got := QuotaWindows(normalized, now); !reflect.DeepEqual(got, wantWindows) {
					t.Fatalf("normalized windows = %+v, want %+v", got, wantWindows)
				}
				wantCount := 2
				if !scenario.observedAt.IsZero() {
					wantCount++
				}
				if len(wantWindows) != wantCount || !wantWindows[len(wantWindows)-1].Scoped {
					t.Fatalf("expected %d windows with scoped window last: %+v", wantCount, wantWindows)
				}
				wantLimit := quotaLimit{ResetsAt: reset, Window: fiveHourWindow}
				for _, auth := range []*Auth{legacy, normalized} {
					if limit, ok := credentialQuotaLimit(auth, "", now); !ok || !reflect.DeepEqual(limit, wantLimit) {
						t.Fatalf("credential quota limit = %+v, %v, want %+v", limit, ok, wantLimit)
					}
				}
			})
		}
	}
}

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
