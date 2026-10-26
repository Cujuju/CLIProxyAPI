package auth

import "time"

// AccountViewAuth returns the credential whose account a client's account calls
// (usage, plan, settings) should reach: the one new requests route to, or, when
// every credential is cooling down, the one that recovers first (the pool's reset).
func AccountViewAuth(strategy, preferredID string, auths []*Auth, now time.Time) *Auth {
	if available, err := getAvailableAuths(auths, "", "", now); err == nil && len(available) > 0 {
		for _, auth := range available {
			if auth.ID == preferredID {
				return auth
			}
		}
		if strategy == SoonestResetStrategy {
			return soonestResetAuth(available, now)
		}
		return available[0]
	}
	var soonest, fallback *Auth
	var soonestAt time.Time
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if fallback == nil {
			fallback = auth
		}
		recoversAt, ok := credentialRecoversAt(auth, now)
		if ok && (soonest == nil || recoversAt.Before(soonestAt)) {
			soonest, soonestAt = auth, recoversAt
		}
	}
	if soonest != nil {
		return soonest
	}
	return fallback
}

// credentialRecoversAt prefers the spent usage window's reset, the time 429 headers report.
func credentialRecoversAt(auth *Auth, now time.Time) (time.Time, bool) {
	if limit, exhausted := credentialQuotaLimit(auth, "", now); exhausted && !limit.ResetsAt.IsZero() {
		return limit.ResetsAt, true
	}
	return CredentialCooldownUntil(auth, now)
}
