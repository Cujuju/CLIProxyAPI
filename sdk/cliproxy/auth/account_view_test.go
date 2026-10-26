package auth

import (
	"strconv"
	"testing"
	"time"
)

// coolingAuth is blocked until retryAt; spentResetsAt, when set, is its spent five-hour window's reset.
func coolingAuth(id string, retryAt, spentResetsAt time.Time) *Auth {
	auth := &Auth{ID: id, Provider: "codex", Status: StatusActive, Unavailable: true, NextRetryAfter: retryAt,
		Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: retryAt}}
	if !spentResetsAt.IsZero() {
		auth.Quota.Signals = map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
			"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0",
			"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(spentResetsAt.Unix(), 10),
		}
		auth.Provider = "claude"
	}
	return auth
}

func TestAccountViewAuth_MatchesTheRoutedPick(t *testing.T) {
	now := soonestResetBase
	auths := []*Auth{
		claudeAuthWithResets("a", now.Add(96*time.Hour), now.Add(time.Hour)),
		claudeAuthWithResets("b", now.Add(25*time.Hour), now.Add(4*time.Hour)),
	}
	for _, strategy := range []string{SoonestResetStrategy, "fill-first"} {
		if got, want := AccountViewAuth(strategy, "", auths, now).ID, PreviewNextPick(strategy, auths, now); got != want {
			t.Fatalf("%s: got %q, want the routed pick %q", strategy, got, want)
		}
	}
	if got := AccountViewAuth(SoonestResetStrategy, "a", auths, now).ID; got != "a" {
		t.Fatalf("got %q, want the preferred credential a", got)
	}
	if got := AccountViewAuth("round-robin", "", auths, now); got == nil {
		t.Fatal("a rotating strategy must still name an available credential")
	}
}

func TestAccountViewAuth_SpentPoolShowsTheFirstReset(t *testing.T) {
	now := soonestResetBase
	auths := []*Auth{
		coolingAuth("late", now.Add(3*time.Hour), time.Time{}),
		coolingAuth("soon", now.Add(time.Hour), time.Time{}),
		{ID: "off", Provider: "codex", Disabled: true},
	}
	if got := AccountViewAuth(SoonestResetStrategy, "late", auths, now).ID; got != "soon" {
		t.Fatalf("got %q, want soon: a cooling preferred credential is not routed to", got)
	}
}

func TestAccountViewAuth_SpentWindowResetOutranksCooldown(t *testing.T) {
	now := soonestResetBase
	// "short" leaves cooldown first but its spent window only resets in four hours.
	auths := []*Auth{
		coolingAuth("short", now.Add(30*time.Minute), now.Add(4*time.Hour)),
		coolingAuth("plain", now.Add(2*time.Hour), time.Time{}),
	}
	if got := AccountViewAuth(SoonestResetStrategy, "", auths, now).ID; got != "plain" {
		t.Fatalf("got %q, want plain (recovers at 2h, before short's 4h window reset)", got)
	}
}

func TestAccountViewAuth_NoCredential(t *testing.T) {
	if got := AccountViewAuth(SoonestResetStrategy, "", []*Auth{{ID: "off", Disabled: true}}, soonestResetBase); got != nil {
		t.Fatalf("got %q, want nil", got.ID)
	}
}
