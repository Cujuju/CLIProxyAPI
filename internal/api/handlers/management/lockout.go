package management

import (
	"crypto/subtle"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// attemptInfo tracks one client IP's failed management keys.
type attemptInfo struct {
	count int
	// lockouts served since the IP last authenticated; selects the next lockout step.
	lockouts     int
	blockedUntil time.Time
	lastActivity time.Time // track last activity for cleanup
}

// attemptCleanupInterval controls how often stale IP entries are purged
const attemptCleanupInterval = 1 * time.Hour

// attemptMaxIdleTime controls how long an IP can be idle before cleanup; cleanup also forgets its served lockouts.
const attemptMaxIdleTime = 2 * time.Hour

// lockoutStep is one rung of the failed-key policy: this many failures earn a lockout of this length.
type lockoutStep struct {
	failures int
	duration time.Duration
}

// lockoutSteps: a first lockout is short, as a mistyped or stale key causes it.
// Once one has been served, fewer failures earn a long one; the last step repeats.
var lockoutSteps = []lockoutStep{
	{failures: 5, duration: 5 * time.Minute},
	{failures: 3, duration: 30 * time.Minute},
}

func stepFor(lockouts int) lockoutStep {
	return lockoutSteps[min(lockouts, len(lockoutSteps)-1)]
}

// lockedFor reports the time left on an IP's lockout. An expired lockout clears its failures but stays served.
func (h *Handler) lockedFor(clientIP string, now time.Time) (time.Duration, bool) {
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	ai := h.failedAttempts[clientIP]
	if ai == nil || ai.blockedUntil.IsZero() {
		return 0, false
	}
	if now.Before(ai.blockedUntil) {
		return ai.blockedUntil.Sub(now), true
	}
	ai.blockedUntil = time.Time{}
	ai.count = 0
	return 0, false
}

func (h *Handler) recordFailure(clientIP string) {
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	ai := h.failedAttempts[clientIP]
	if ai == nil {
		ai = &attemptInfo{}
		h.failedAttempts[clientIP] = ai
	}
	now := time.Now()
	ai.count++
	ai.lastActivity = now
	if step := stepFor(ai.lockouts); ai.count >= step.failures {
		ai.blockedUntil = now.Add(step.duration)
		ai.count = 0
		ai.lockouts++
	}
}

// clearFailures forgets an IP after it authenticates, so its next lockout starts at the first step.
func (h *Handler) clearFailures(clientIP string) {
	h.attemptsMu.Lock()
	delete(h.failedAttempts, clientIP)
	h.attemptsMu.Unlock()
}

// ClientKeyAsManagementKeyMessage answers a client key sent where the management key belongs.
const ClientKeyAsManagementKeyMessage = "this is a client API key; management calls need the management key"

// isClientAPIKey reports a proxy client key sent as a management key: a setup mix-up, not a guess.
func (h *Handler) isClientAPIKey(provided string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return false
	}
	for _, key := range h.cfg.APIKeys {
		if key = strings.TrimSpace(key); key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(provided)) == 1 {
			return true
		}
	}
	return false
}

// LockoutStatus is one client IP with failed management keys.
type LockoutStatus struct {
	IP       string `json:"ip"`
	Failures int    `json:"failures"`
	// FailuresAllowed is how many failures in total earn the next lockout.
	FailuresAllowed int        `json:"failures_allowed"`
	LockedUntil     *time.Time `json:"locked_until"`
	Lockouts        int        `json:"lockouts"`
}

func (h *Handler) lockoutStatuses(now time.Time) []LockoutStatus {
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	out := make([]LockoutStatus, 0, len(h.failedAttempts))
	for ip, ai := range h.failedAttempts {
		status := LockoutStatus{IP: ip, Failures: ai.count, FailuresAllowed: stepFor(ai.lockouts).failures, Lockouts: ai.lockouts}
		if now.Before(ai.blockedUntil) {
			until := ai.blockedUntil
			status.LockedUntil = &until
		}
		if status.Failures == 0 && status.LockedUntil == nil {
			continue
		}
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// GetLockouts lists client IPs with failed management keys or an active lockout.
func (h *Handler) GetLockouts(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"lockouts": h.lockoutStatuses(time.Now())})
}

// DeleteLockouts lifts every lockout and forgets all failed attempts.
func (h *Handler) DeleteLockouts(c *gin.Context) {
	h.attemptsMu.Lock()
	h.failedAttempts = make(map[string]*attemptInfo)
	h.attemptsMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
