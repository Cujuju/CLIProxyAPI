package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	lockoutTestIP     = "127.0.0.1"
	lockoutTestSecret = "test-secret"
	lockoutClientKey  = "client-key"
)

func newLockoutHandler() *Handler {
	cfg := &config.Config{}
	cfg.APIKeys = []string{lockoutClientKey}
	return &Handler{cfg: cfg, failedAttempts: make(map[string]*attemptInfo), envSecret: lockoutTestSecret}
}

func failTimes(t *testing.T, h *Handler, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if allowed, status, _ := h.AuthenticateManagementKey(lockoutTestIP, true, "wrong"); allowed || status != http.StatusUnauthorized {
			t.Fatalf("failure %d: allowed=%v status=%d, want 401", i+1, allowed, status)
		}
	}
}

func lockedOut(h *Handler) bool {
	_, status, _ := h.AuthenticateManagementKey(lockoutTestIP, true, lockoutTestSecret)
	return status == http.StatusForbidden
}

// expireLockout moves the active lockout into the past, as if its time had run out.
func expireLockout(h *Handler) {
	h.failedAttempts[lockoutTestIP].blockedUntil = time.Now().Add(-time.Second)
}

func TestLockoutSteps(t *testing.T) {
	h := newLockoutHandler()
	first, repeat := lockoutSteps[0], lockoutSteps[1]

	failTimes(t, h, first.failures-1)
	if lockedOut(h) {
		t.Fatal("locked out before the first step's failures")
	}
	h.clearFailures(lockoutTestIP) // lockedOut authenticated, which forgets the failures
	failTimes(t, h, first.failures)
	if !lockedOut(h) {
		t.Fatal("not locked out after the first step's failures")
	}
	if got := time.Until(h.failedAttempts[lockoutTestIP].blockedUntil); got > first.duration {
		t.Fatalf("first lockout lasts %s, want at most %s", got, first.duration)
	}

	expireLockout(h)
	failTimes(t, h, repeat.failures)
	if !lockedOut(h) {
		t.Fatal("not locked out after the repeat step's failures")
	}
	if got := time.Until(h.failedAttempts[lockoutTestIP].blockedUntil); got <= first.duration || got > repeat.duration {
		t.Fatalf("repeat lockout lasts %s, want the repeat step's %s", got, repeat.duration)
	}

	// The last step repeats.
	expireLockout(h)
	failTimes(t, h, repeat.failures)
	if !lockedOut(h) {
		t.Fatal("the last step did not repeat")
	}
}

func TestLockoutSuccessReturnsToFirstStep(t *testing.T) {
	h := newLockoutHandler()
	failTimes(t, h, lockoutSteps[0].failures)
	expireLockout(h)
	if allowed, _, _ := h.AuthenticateManagementKey(lockoutTestIP, true, lockoutTestSecret); !allowed {
		t.Fatal("correct key denied after the lockout expired")
	}
	failTimes(t, h, lockoutSteps[1].failures)
	if lockedOut(h) {
		t.Fatal("a success did not return the IP to the first step")
	}
}

func TestClientKeyAsManagementKeyEarnsNoLockout(t *testing.T) {
	h := newLockoutHandler()
	for i := 0; i < lockoutSteps[0].failures*2; i++ {
		allowed, status, msg := h.AuthenticateManagementKey(lockoutTestIP, true, lockoutClientKey)
		if allowed || status != http.StatusUnauthorized || msg != ClientKeyAsManagementKeyMessage {
			t.Fatalf("attempt %d: allowed=%v status=%d msg=%q", i+1, allowed, status, msg)
		}
	}
	if lockedOut(h) {
		t.Fatal("a client key in the management field caused a lockout")
	}
}

func TestLockoutEndpoints(t *testing.T) {
	h := newLockoutHandler()
	failTimes(t, h, lockoutSteps[0].failures)
	engine := gin.New()
	engine.GET("/lockouts", h.GetLockouts)
	engine.DELETE("/lockouts", h.DeleteLockouts)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lockouts", nil))
	var body struct{ Lockouts []LockoutStatus }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Lockouts) != 1 || body.Lockouts[0].IP != lockoutTestIP || body.Lockouts[0].LockedUntil == nil ||
		body.Lockouts[0].FailuresAllowed != lockoutSteps[1].failures {
		t.Fatalf("lockouts = %+v", body.Lockouts)
	}

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/lockouts", nil))
	if lockedOut(h) {
		t.Fatal("DELETE did not lift the lockout")
	}
	if got := h.lockoutStatuses(time.Now()); len(got) != 0 {
		t.Fatalf("after DELETE: %+v", got)
	}
}
