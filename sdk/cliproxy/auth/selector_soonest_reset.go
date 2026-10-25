package auth

import (
	"context"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetStrategy is the routing.strategy value for SoonestResetSelector.
const SoonestResetStrategy = "soonest-reset"

// SoonestResetSelector spends the allowance that expires first. It prefers the
// credential whose longest account window (weekly for Claude) resets soonest,
// breaks ties on the shortest window (5-hour), then on credential ID.
// Credentials without a known future reset rank after all known ones.
//
// Benched credentials are already excluded by availability filtering, so a
// limit hit moves traffic to the next-ranked credential, and the credential
// rejoins the ranking once its cooldown ends.
type SoonestResetSelector struct {
	// now is a test clock; nil uses time.Now.
	now func() time.Time
}

// Pick selects the available credential whose allowance resets soonest.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	if s != nil && s.now != nil {
		now = s.now()
	}
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return soonestResetAuth(available, now), nil
}

// soonestResetAuth ranks an ID-sorted, non-empty candidate list. Iterating in
// order and replacing only on a strictly better rank keeps ID order on ties.
func soonestResetAuth(available []*Auth, now time.Time) *Auth {
	best := available[0]
	bestLong, bestShort := accountResetTimes(best, now)
	for _, candidate := range available[1:] {
		long, short := accountResetTimes(candidate, now)
		if resetsEarlier(long, bestLong) || (long.Equal(bestLong) && resetsEarlier(short, bestShort)) {
			best, bestLong, bestShort = candidate, long, short
		}
	}
	return best
}

// resetsEarlier orders known times before unknown (zero) ones.
func resetsEarlier(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	if b.IsZero() {
		return true
	}
	return a.Before(b)
}
