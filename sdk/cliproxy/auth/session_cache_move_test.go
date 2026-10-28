package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSessionCacheMoveSession(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	cache.setSessionAliases("a", "claude", "claude:s1", "mixed::claude:s1::opus")
	cache.setSessionAliases("a", "claude", "claude:s1", "mixed::claude:s1::haiku")
	cache.setSessionAliases("a", "claude", "claude:s1:agent:x", "mixed::claude:s1:agent:x::opus")
	cache.setSessionAliases("a", "claude", "claude:s10", "mixed::claude:s10::opus")
	cache.setSessionAliases("codex-1", "codex", "claude:s1", "codex::claude:s1::gpt")
	claudeAuths := map[string]struct{}{"a": {}, "b": {}}

	if got := cache.MoveSession("claude:s1", "b", claudeAuths); got != 3 {
		t.Fatalf("MoveSession bound = %d, want 3 (two models and the subagent)", got)
	}
	for key, want := range map[string]string{
		"mixed::claude:s1::opus":         "b",
		"mixed::claude:s1::haiku":        "b",
		"mixed::claude:s1:agent:x::opus": "b",
		"mixed::claude:s10::opus":        "a",
		"codex::claude:s1::gpt":          "codex-1",
	} {
		if got, _ := cache.Get(key); got != want {
			t.Errorf("%s bound to %q, want %q", key, got, want)
		}
	}
	if got := cache.MoveSession("claude:s1", "b", claudeAuths); got != 3 {
		t.Fatalf("repeat MoveSession bound = %d, want 3", got)
	}
	if got := cache.MoveSession("claude:unknown", "b", claudeAuths); got != 0 {
		t.Fatalf("unknown session bound = %d, want 0", got)
	}
}

func TestManagerMoveSessionRoutesNextRequest(t *testing.T) {
	const model = "move-session-model"
	m := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	defer affinity.Stop()
	m.SetSelector(affinity)
	var used []string
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude", executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		used = append(used, auth.ID)
		return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
	}})
	reg := registry.GetGlobalRegistry()
	for _, a := range []*Auth{
		{ID: "move-high", Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "10"}},
		{ID: "move-low", Provider: "claude", Status: StatusActive},
	} {
		reg.RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: model}})
		id := a.ID
		t.Cleanup(func() { reg.UnregisterClient(id) })
		if _, err := m.Register(WithSkipPersist(context.Background()), a); err != nil {
			t.Fatal(err)
		}
	}
	const claudeSession = "5c8e2a4e-1f0b-4c7a-9d2e-3b6f0a1c2d3e"
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {claudeSession}}}
	execute := func() string {
		t.Helper()
		if _, err := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts); err != nil {
			t.Fatal(err)
		}
		return used[len(used)-1]
	}
	if got := execute(); got != "move-high" {
		t.Fatalf("first request on %s, want move-high", got)
	}
	low, _ := m.GetByID("move-low")
	if bound, ok := m.MoveSession("claude:"+claudeSession, low); !ok || bound == 0 {
		t.Fatalf("MoveSession = %d, %v; want a moved binding", bound, ok)
	}
	for range 2 {
		if got := execute(); got != "move-low" {
			t.Fatalf("request after move on %s, want move-low", got)
		}
	}
}
