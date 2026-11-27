package cliproxy

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Quota-only mode (antigravity.direct-api off) never calls Google's Cloud Code
// API: no executor, no models, no probes. Quota comes from the official CLI,
// which reports only the account it is signed into.

const (
	antigravityProvider = "antigravity"
	// antigravityCLIQuotaInterval spaces background CLI runs. A run takes about
	// 3s, starts no model turn and runs off every request path.
	antigravityCLIQuotaInterval = 5 * time.Minute
)

var (
	errAntigravityCLIQuotaNoCredential = errors.New("antigravity quota-only mode needs one enabled Antigravity OAuth credential")
	errAntigravityCLIQuotaAmbiguous    = errors.New("antigravity quota-only mode supports one enabled credential: the CLI reports only its own account")
)

// antigravityCLIQuotaFetcher reads quota from the official CLI. Tests replace it.
var antigravityCLIQuotaFetcher = sdkauth.FetchAntigravityCLIQuota

// antigravityCLIQuota schedules CLI runs. One CLI login serves the process, like
// the package-level capability cache serves direct mode.
var antigravityCLIQuota antigravityCLIQuotaSchedule

type antigravityCLIQuotaSchedule struct {
	run     sync.Mutex                // serializes CLI runs
	last    antigravityCLIQuotaResult // guarded by run
	mu      sync.Mutex                // guards the fields below
	nextRun time.Time
	failure antigravityFailureState
}

// antigravityCLIQuotaResult is the outcome of the latest CLI run.
type antigravityCLIQuotaResult struct {
	finishedAt time.Time
	shareable  bool // false when the runner's own context ended the run
	id         string
	err        error
}

func (s *Service) antigravityDirectAPIEnabled() bool {
	return s.currentConfig().AntigravityDirectAPIEnabled()
}

// retireAntigravityExecutor removes the executor so nothing can route to,
// refresh or probe Antigravity credentials through it.
func (s *Service) retireAntigravityExecutor() {
	if s == nil || s.coreManager == nil {
		return
	}
	s.coreManager.UnregisterExecutor(antigravityProvider)
	delete(s.ownedExecutors, antigravityProvider)
	delete(s.ownedRevisions, antigravityProvider)
}

func isAntigravityOAuthCredential(auth *coreauth.Auth) bool {
	return auth != nil && !isAntigravityAuthDisabled(auth) &&
		strings.EqualFold(strings.TrimSpace(auth.Provider), antigravityProvider) &&
		auth.AuthKind() == coreauth.AuthKindOAuth && strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey]) == ""
}

// antigravityCLIQuotaCredential returns the single credential the CLI's quota describes.
func (s *Service) antigravityCLIQuotaCredential() (*coreauth.Auth, error) {
	var found *coreauth.Auth
	for _, auth := range s.coreManager.List() {
		if !isAntigravityOAuthCredential(auth) {
			continue
		}
		if found != nil {
			return nil, errAntigravityCLIQuotaAmbiguous
		}
		found = auth
	}
	if found == nil {
		return nil, errAntigravityCLIQuotaNoCredential
	}
	return found, nil
}

// refreshAntigravityCLIQuota runs the CLI once and records its windows on the
// single eligible credential. It returns that credential's ID when one was bound.
// A caller that waited on a run in flight shares its result instead of running
// the CLI again, so a refresh never waits for two runs.
func (s *Service) refreshAntigravityCLIQuota(ctx context.Context) (string, error) {
	calledAt := antigravityNowFunc()
	antigravityCLIQuota.run.Lock()
	defer antigravityCLIQuota.run.Unlock()
	if last := antigravityCLIQuota.last; last.shareable && last.finishedAt.After(calledAt) {
		return last.id, last.err
	}
	auth, err := s.antigravityCLIQuotaCredential()
	if err != nil {
		return "", err
	}
	requestedAt := antigravityNowFunc()
	buckets, err := antigravityCLIQuotaFetcher(ctx)
	if err == nil {
		lookedUpAt := antigravityNowFunc()
		_, err = s.coreManager.ObserveQuotaLookupWindows(ctx, auth.ID, antigravitySummaryWindows(buckets, requestedAt), lookedUpAt,
			func(current *coreauth.Auth) (bool, error) {
				if !isAntigravityOAuthCredential(current) {
					return false, coreauth.ErrQuotaLookupCredentialChanged
				}
				return !lookedUpAt.Before(current.Quota.LookedUpAt), nil
			})
	}
	finishedAt := antigravityNowFunc()
	antigravityCLIQuota.last = antigravityCLIQuotaResult{finishedAt: finishedAt, shareable: ctx.Err() == nil, id: auth.ID, err: err}
	antigravityCLIQuota.finish(finishedAt, err == nil)
	return auth.ID, err
}

// finish schedules the next run: the CLI interval after success, the shared
// transient backoff after failure.
func (q *antigravityCLIQuotaSchedule) finish(now time.Time, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ok {
		q.failure = antigravityFailureState{}
		q.nextRun = now.Add(antigravityCLIQuotaInterval)
		return
	}
	q.failure = nextAntigravityFailure(q.failure, now, rand.Int64N)
	q.nextRun = q.failure.nextRetry
}

func (q *antigravityCLIQuotaSchedule) next() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.nextRun
}

// refreshAntigravityCLIQuotaIfDue is the background loop's quota-only scan.
func (s *Service) refreshAntigravityCLIQuotaIfDue(ctx context.Context) {
	if antigravityNowFunc().Before(antigravityCLIQuota.next()) {
		return
	}
	_, _ = s.refreshAntigravityCLIQuota(ctx)
}

// nextAntigravityCLIQuotaDelay mirrors direct mode: a past deadline runs on the
// next scan tick, so a missing credential never spins the loop.
func (s *Service) nextAntigravityCLIQuotaDelay() time.Duration {
	now := antigravityNowFunc()
	if next := antigravityCLIQuota.next(); next.After(now) && next.Before(now.Add(antigravityRefreshScanInterval)) {
		return next.Sub(now)
	}
	return antigravityRefreshScanInterval
}

// refreshAllAntigravityCLIQuota reports a binding error on every eligible credential.
func (s *Service) refreshAllAntigravityCLIQuota(ctx context.Context) map[string]error {
	failures := map[string]error{}
	id, err := s.refreshAntigravityCLIQuota(ctx)
	switch {
	case err == nil, errors.Is(err, errAntigravityCLIQuotaNoCredential):
	case id != "":
		failures[id] = err
	default:
		for _, auth := range s.coreManager.List() {
			if isAntigravityOAuthCredential(auth) {
				failures[auth.ID] = err
			}
		}
	}
	return failures
}
