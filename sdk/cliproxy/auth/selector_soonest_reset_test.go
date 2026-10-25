package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var soonestResetBase = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func claudeAuthWithResets(id string, weekly, fiveHour time.Time) *Auth {
	signals := map[string]string{}
	if !weekly.IsZero() {
		signals["Anthropic-Ratelimit-Unified-7d-Reset"] = strconv.FormatInt(weekly.Unix(), 10)
		signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = "0.40"
	}
	if !fiveHour.IsZero() {
		signals["Anthropic-Ratelimit-Unified-5h-Reset"] = strconv.FormatInt(fiveHour.Unix(), 10)
		signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = "0.10"
	}
	return &Auth{ID: id, Provider: "claude", Quota: QuotaState{Signals: signals, ObservedAt: soonestResetBase}}
}

func pickAt(t *testing.T, now time.Time, auths []*Auth) string {
	t.Helper()
	selector := &SoonestResetSelector{now: func() time.Time { return now }}
	picked, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	return picked.ID
}

func TestSoonestResetPicksEarliestWeeklyReset(t *testing.T) {
	auths := []*Auth{
		claudeAuthWithResets("a", soonestResetBase.Add(96*time.Hour), soonestResetBase.Add(time.Hour)),
		claudeAuthWithResets("b", soonestResetBase.Add(25*time.Hour), soonestResetBase.Add(4*time.Hour)),
		claudeAuthWithResets("c", soonestResetBase.Add(60*time.Hour), soonestResetBase.Add(2*time.Hour)),
	}
	if got := pickAt(t, soonestResetBase, auths); got != "b" {
		t.Fatalf("picked %q, want b (soonest weekly reset)", got)
	}
}

func TestSoonestResetBreaksWeeklyTieOnFiveHourReset(t *testing.T) {
	weekly := soonestResetBase.Add(48 * time.Hour)
	auths := []*Auth{
		claudeAuthWithResets("a", weekly, soonestResetBase.Add(3*time.Hour)),
		claudeAuthWithResets("b", weekly, soonestResetBase.Add(time.Hour)),
	}
	if got := pickAt(t, soonestResetBase, auths); got != "b" {
		t.Fatalf("picked %q, want b (sooner 5-hour reset)", got)
	}
}

func TestSoonestResetRanksUnknownAndPastResetsLast(t *testing.T) {
	auths := []*Auth{
		{ID: "a", Provider: "claude"},
		claudeAuthWithResets("b", soonestResetBase.Add(-time.Hour), time.Time{}),
		claudeAuthWithResets("c", soonestResetBase.Add(120*time.Hour), time.Time{}),
	}
	if got := pickAt(t, soonestResetBase, auths); got != "c" {
		t.Fatalf("picked %q, want c (only known future reset)", got)
	}
	unknownOnly := []*Auth{{ID: "b", Provider: "claude"}, {ID: "a", Provider: "claude"}}
	if got := pickAt(t, soonestResetBase, unknownOnly); got != "a" {
		t.Fatalf("picked %q, want a (ID order when nothing is known)", got)
	}
}

func TestSoonestResetSwapsOnLimitAndReturnsAfterCooldown(t *testing.T) {
	recoverAt := soonestResetBase.Add(time.Hour)
	limited := claudeAuthWithResets("a", soonestResetBase.Add(24*time.Hour), recoverAt)
	limited.Quota.Exceeded = true
	limited.Quota.Reason = "credential_quota"
	limited.Quota.NextRecoverAt = recoverAt
	auths := []*Auth{limited, claudeAuthWithResets("b", soonestResetBase.Add(72*time.Hour), soonestResetBase.Add(2*time.Hour))}

	if got := pickAt(t, soonestResetBase, auths); got != "b" {
		t.Fatalf("picked %q while a is benched, want b", got)
	}
	// After the 5-hour reset passes, a is eligible again and still has the soonest weekly reset.
	if got := pickAt(t, recoverAt.Add(time.Minute), auths); got != "a" {
		t.Fatalf("picked %q after cooldown, want a", got)
	}
}

func TestSoonestResetRespectsPriorityTiers(t *testing.T) {
	low := claudeAuthWithResets("a", soonestResetBase.Add(time.Hour), time.Time{})
	high := claudeAuthWithResets("b", soonestResetBase.Add(100*time.Hour), time.Time{})
	high.Attributes = map[string]string{"priority": "5"}
	if got := pickAt(t, soonestResetBase, []*Auth{low, high}); got != "b" {
		t.Fatalf("picked %q, want b (higher priority tier wins first)", got)
	}
}

func TestSoonestResetUsesCodexWindowMinutes(t *testing.T) {
	codex := func(id string, weeklyReset time.Time) *Auth {
		return &Auth{ID: id, Provider: "codex", Quota: QuotaState{ObservedAt: soonestResetBase, Signals: map[string]string{
			"X-Codex-Primary-Window-Minutes":   "300",
			"X-Codex-Primary-Reset-At":         strconv.FormatInt(soonestResetBase.Add(time.Hour).Unix(), 10),
			"X-Codex-Secondary-Window-Minutes": "10080",
			"X-Codex-Secondary-Reset-At":       strconv.FormatInt(weeklyReset.Unix(), 10),
		}}}
	}
	selector := &SoonestResetSelector{now: func() time.Time { return soonestResetBase }}
	auths := []*Auth{codex("a", soonestResetBase.Add(90*time.Hour)), codex("b", soonestResetBase.Add(30*time.Hour))}
	picked, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if picked.ID != "b" {
		t.Fatalf("picked %q, want b (soonest weekly window)", picked.ID)
	}
}

func TestSoonestResetUnderSessionAffinityKeepsBindingForExistingSessions(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
	defer selector.Stop()

	first := claudeAuthWithResets("a", now.Add(24*time.Hour), time.Time{})
	second := claudeAuthWithResets("b", now.Add(72*time.Hour), time.Time{})
	session := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_x_account__session_soonest-reset-existing"}}`)}

	bound, err := selector.Pick(context.Background(), "claude", "claude-x", session, []*Auth{first, second})
	if err != nil || bound.ID != "a" {
		t.Fatalf("first pick = %v, %v; want a", bound, err)
	}

	// b now resets sooner, but the existing conversation keeps its account.
	first = claudeAuthWithResets("a", now.Add(96*time.Hour), time.Time{})
	second = claudeAuthWithResets("b", now.Add(12*time.Hour), time.Time{})
	again, err := selector.Pick(context.Background(), "claude", "claude-x", session, []*Auth{first, second})
	if err != nil || again.ID != "a" {
		t.Fatalf("existing session pick = %v, %v; want a", again, err)
	}

	fresh := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_x_account__session_soonest-reset-new"}}`)}
	newPick, err := selector.Pick(context.Background(), "claude", "claude-x", fresh, []*Auth{first, second})
	if err != nil || newPick.ID != "b" {
		t.Fatalf("new session pick = %v, %v; want b", newPick, err)
	}
}

func TestPreviewNextPick(t *testing.T) {
	auths := []*Auth{
		claudeAuthWithResets("a", soonestResetBase.Add(80*time.Hour), time.Time{}),
		claudeAuthWithResets("b", soonestResetBase.Add(20*time.Hour), time.Time{}),
	}
	if got := PreviewNextPick(SoonestResetStrategy, auths, soonestResetBase); got != "b" {
		t.Fatalf("soonest-reset preview = %q, want b", got)
	}
	if got := PreviewNextPick("fill-first", auths, soonestResetBase); got != "a" {
		t.Fatalf("fill-first preview = %q, want a", got)
	}
	if got := PreviewNextPick("round-robin", auths, soonestResetBase); got != "" {
		t.Fatalf("round-robin preview = %q, want empty", got)
	}
}

func TestQuotaWindowsNormalizesClaudeSignals(t *testing.T) {
	auth := claudeAuthWithResets("a", soonestResetBase.Add(48*time.Hour), soonestResetBase.Add(time.Hour))
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] = "allowed_warning"
	windows := QuotaWindows(auth, soonestResetBase)
	if len(windows) != 2 || windows[0].Key != "five_hour" || windows[1].Key != "seven_day" {
		t.Fatalf("windows = %+v, want five_hour then seven_day", windows)
	}
	if windows[0].UsedPercent == nil || *windows[0].UsedPercent != 10 || windows[0].Status != "allowed_warning" {
		t.Fatalf("five_hour window = %+v, want 10%% allowed_warning", windows[0])
	}
}
