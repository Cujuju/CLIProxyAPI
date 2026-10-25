package management

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const testSupervisorPassword = "0123456789abcdef0123456789abcdef"

func newSupervisorHandler() *Handler {
	h := &Handler{cfg: &config.Config{}, failedAttempts: make(map[string]*attemptInfo)}
	h.SetSupervisorPassword(testSupervisorPassword)
	return h
}

func banLoopback(t *testing.T, h *Handler) {
	t.Helper()
	for i := 0; i < 5; i++ {
		if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong"); allowed {
			t.Fatalf("wrong key accepted at attempt %d", i+1)
		}
	}
	if _, status, msg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong"); status != http.StatusForbidden || !strings.HasPrefix(msg, "IP banned") {
		t.Fatalf("expected ban, got status=%d msg=%q", status, msg)
	}
}

func TestSupervisorPasswordWorksWithoutConfigSecret(t *testing.T) {
	h := newSupervisorHandler()
	if allowed, status, msg := h.AuthenticateManagementKey("127.0.0.1", true, testSupervisorPassword); !allowed {
		t.Fatalf("supervisor denied: status=%d msg=%q", status, msg)
	}
}

func TestSupervisorPasswordSkipsLoopbackBan(t *testing.T) {
	h := newSupervisorHandler()
	banLoopback(t, h)
	if allowed, status, msg := h.AuthenticateManagementKey("127.0.0.1", true, testSupervisorPassword); !allowed {
		t.Fatalf("supervisor locked out by ban: status=%d msg=%q", status, msg)
	}
	// The ban still applies to every other key.
	if allowed, status, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong"); allowed || status != http.StatusForbidden {
		t.Fatalf("ban lifted for other keys: allowed=%v status=%d", allowed, status)
	}
}

func TestSupervisorPasswordIsLoopbackOnly(t *testing.T) {
	h := newSupervisorHandler()
	h.cfg.RemoteManagement.AllowRemote = true
	if allowed, _, _ := h.AuthenticateManagementKey("192.168.1.20", false, testSupervisorPassword); allowed {
		t.Fatal("supervisor password accepted from a remote address")
	}
}

func TestLocalPasswordWorksWithoutConfigSecret(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, failedAttempts: make(map[string]*attemptInfo)}
	h.SetLocalPassword("tui-password")
	if allowed, status, msg := h.AuthenticateManagementKey("127.0.0.1", true, "tui-password"); !allowed {
		t.Fatalf("local password denied: status=%d msg=%q", status, msg)
	}
	if allowed, status, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong"); allowed || status != http.StatusUnauthorized {
		t.Fatalf("wrong key: allowed=%v status=%d, want 401", allowed, status)
	}
}
