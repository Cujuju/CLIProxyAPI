package auth

import (
	"strconv"
	"testing"
	"time"
)

func codexSessionAuth(observedAt time.Time, used string, primaryReset, weeklyReset time.Time) *Auth {
	return &Auth{ID: "codex", Provider: "codex", Quota: QuotaState{ObservedAt: observedAt, Signals: map[string]string{
		"X-Codex-Primary-Used-Percent":     used,
		"X-Codex-Primary-Window-Minutes":   "300",
		"X-Codex-Primary-Reset-At":         strconv.FormatInt(primaryReset.Unix(), 10),
		"X-Codex-Secondary-Used-Percent":   "0",
		"X-Codex-Secondary-Window-Minutes": "10080",
		"X-Codex-Secondary-Reset-At":       strconv.FormatInt(weeklyReset.Unix(), 10),
	}}}
}

func windowByKey(t *testing.T, auth *Auth, key string) QuotaWindow {
	t.Helper()
	for _, window := range QuotaWindows(auth, soonestResetBase) {
		if window.Key == key {
			return window
		}
	}
	t.Fatalf("window %q missing", key)
	return QuotaWindow{}
}

func TestCodexFiveHourWindowStartsOnUse(t *testing.T) {
	weekly := soonestResetBase.Add(100 * time.Hour)
	unstarted := codexSessionAuth(soonestResetBase, "0", soonestResetBase.Add(fiveHourWindow), weekly)
	primary := windowByKey(t, unstarted, "primary")
	if primary.Cadence != ResetCadenceOnUse {
		t.Fatalf("primary cadence = %q, want on_use", primary.Cadence)
	}
	if !primary.ResetsAt.IsZero() {
		t.Fatalf("unstarted primary resets at %v, want no reset", primary.ResetsAt)
	}
	secondary := windowByKey(t, unstarted, "secondary")
	if secondary.Cadence != ResetCadenceUnknown || !secondary.ResetsAt.Equal(time.Unix(weekly.Unix(), 0)) {
		t.Fatalf("weekly window changed: cadence %q, reset %v", secondary.Cadence, secondary.ResetsAt)
	}
}

func TestCodexFiveHourWindowKeepsResetOnceStarted(t *testing.T) {
	weekly := soonestResetBase.Add(100 * time.Hour)
	for name, auth := range map[string]*Auth{
		"used":           codexSessionAuth(soonestResetBase, "3", soonestResetBase.Add(4*time.Hour), weekly),
		"unused but old": codexSessionAuth(soonestResetBase, "0", soonestResetBase.Add(fiveHourWindow-2*codexUnstartedTolerance), weekly),
	} {
		primary := windowByKey(t, auth, "primary")
		if primary.ResetsAt.IsZero() {
			t.Fatalf("%s: started window lost its reset", name)
		}
	}
}
