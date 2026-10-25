package auth

import (
	"net/http"
	"strings"
	"time"
)

// CredentialCooldownUntil reports when a credential that is currently benched
// becomes eligible again. ok is false when the credential is not benched or
// the recovery time is unknown.
func CredentialCooldownUntil(auth *Auth, now time.Time) (time.Time, bool) {
	blocked, _, next := isAuthBlockedForModel(auth, "", now)
	if !blocked || next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

// PreviewNextPick returns the credential ID the given strategy would choose
// for a new conversation among auths, without touching selector state.
// Rotating strategies have no deterministic answer and return "".
func PreviewNextPick(strategy string, auths []*Auth, now time.Time) string {
	available, err := getAvailableAuths(auths, "", "", now)
	if err != nil || len(available) == 0 {
		return ""
	}
	switch strategy {
	case SoonestResetStrategy:
		return soonestResetAuth(available, now).ID
	case "fill-first":
		return available[0].ID
	default:
		return ""
	}
}

// ObserveQuotaHeaders records an out-of-band quota snapshot, such as a usage
// lookup, for one credential. Older snapshots never replace newer ones.
func (m *Manager) ObserveQuotaHeaders(authID string, headers http.Header, observedAt time.Time) bool {
	if m == nil || strings.TrimSpace(authID) == "" || len(headers) == 0 {
		return false
	}
	release := m.lockAuthMutation(authID)
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	auth := m.auths[authID]
	if auth == nil || observedAt.Before(auth.Quota.ObservedAt) {
		return false
	}
	return auth.Quota.ObserveResponseHeadersForProvider(auth.Provider, headers, observedAt)
}
