package management

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestMoveSession(t *testing.T) {
	h := newDesktopUsageHandler(t, "sr")
	post := func(body string) int {
		t.Helper()
		return serveDesktop(t, h.MoveSession, http.MethodPost, body, "127.0.0.1:1").Code
	}
	if got := post(`{"session_id":"claude:s1","auth_index":"idx-late"}`); got != http.StatusConflict {
		t.Fatalf("affinity off: status = %d, want 409", got)
	}

	affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.RoundRobinSelector{}, TTL: time.Hour})
	defer affinity.Stop()
	h.authManager.SetSelector(affinity)
	affinity.RestoreSessionBindings([]coreauth.SessionBindingRecord{
		{AuthID: "soon", ExpiresAt: time.Now().Add(time.Hour), Aliases: []string{"mixed::claude:s1::claude-model"}, SessionID: "claude:s1", Provider: "claude"},
	})

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"auth_index":"idx-late"}`, http.StatusBadRequest},
		{`{"session_id":"claude:s1","auth_index":"missing"}`, http.StatusNotFound},
		{`{"session_id":"claude:s1","auth_index":"idx-codex"}`, http.StatusNotFound},
		{`{"session_id":"claude:other","auth_index":"idx-late"}`, http.StatusNotFound},
		{`{"session_id":"claude:s1","auth_index":"idx-late"}`, http.StatusOK},
	} {
		if got := post(tc.body); got != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.body, got, tc.want)
		}
	}
	bindings := affinity.SessionBindings()
	if len(bindings) != 1 || bindings[0].AuthID != "late" {
		t.Fatalf("bindings after move = %+v, want one on late", bindings)
	}
}
