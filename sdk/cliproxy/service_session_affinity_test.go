package cliproxy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A restart (new process: new selector and persister) keeps a conversation on its credential,
// even when the strategy would now pick another one.
func TestSessionAffinityPersisterKeepsConversationAcrossRestart(t *testing.T) {
	store := coreauth.NewFileSessionAffinityStore(filepath.Join(t.TempDir(), coreauth.SessionAffinityStateFile))
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_x_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)}
	pick := func(selector *coreauth.SessionAffinitySelector, auths ...*coreauth.Auth) string {
		t.Helper()
		got, err := selector.Pick(context.Background(), "claude", "claude-opus", opts, auths)
		if err != nil || got == nil {
			t.Fatalf("Pick: %v %v", got, err)
		}
		return got.ID
	}
	newSelector := func() *coreauth.SessionAffinitySelector {
		selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.FillFirstSelector{}, TTL: time.Hour})
		t.Cleanup(selector.Stop)
		return selector
	}
	authA, authB := &coreauth.Auth{ID: "auth-a"}, &coreauth.Auth{ID: "auth-b"}

	before := newSelector()
	if got := pick(before, authB); got != "auth-b" {
		t.Fatalf("first pick = %s, want auth-b", got)
	}
	(&sessionAffinityPersister{}).sync(before, store)

	after := newSelector()
	(&sessionAffinityPersister{}).sync(after, store)
	if got := pick(after, authA, authB); got != "auth-b" {
		t.Fatalf("after restart = %s, want auth-b (fill-first alone picks auth-a)", got)
	}
}

// A routing change rebuilds the selector; the persister carries live bindings into it.
func TestSessionAffinityPersisterCarriesBindingsToReplacementSelector(t *testing.T) {
	store := coreauth.NewFileSessionAffinityStore(filepath.Join(t.TempDir(), coreauth.SessionAffinityStateFile))
	persister := &sessionAffinityPersister{}
	old := coreauth.NewSessionAffinitySelector(&coreauth.FillFirstSelector{})
	t.Cleanup(old.Stop)
	persister.sync(old, store)
	if restored := old.RestoreSessionBindings([]coreauth.SessionBindingRecord{{AuthID: "auth-b", ExpiresAt: time.Now().Add(time.Hour), Aliases: []string{"claude::claude:s::m"}}}); restored != 1 {
		t.Fatalf("seed restored %d, want 1", restored)
	}
	persister.sync(old, store)

	replacement := coreauth.NewSessionAffinitySelector(&coreauth.FillFirstSelector{})
	t.Cleanup(replacement.Stop)
	persister.sync(replacement, store)
	if bindings := replacement.SessionBindings(); len(bindings) != 1 || bindings[0].AuthID != "auth-b" {
		t.Fatalf("replacement bindings = %+v, want the auth-b binding", bindings)
	}
}
