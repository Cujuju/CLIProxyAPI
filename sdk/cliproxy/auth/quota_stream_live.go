package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// observeStreamQuota publishes watermarks when received, independently of stream completion.
func (m *Manager) observeStreamQuota(ctx context.Context, auth *Auth, optsHeaders http.Header) context.Context {
	if m.HomeEnabled() || auth == nil || !ProviderSupportsQuotaObservation(auth.Provider) {
		return ctx
	}
	stamp := Result{AuthID: auth.ID, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, AccessToken: authAccessTokenForRequest(ctx, auth, optsHeaders)}
	var responseEndOnce sync.Once
	return logging.WithResponseHeadersObserver(ctx, func(headers http.Header, at time.Time) {
		release := m.lockAuthMutation(auth.ID)
		m.mu.Lock()
		current := m.auths[auth.ID]
		changed := false
		recorded := false
		if current != nil && !isStaleExecutionResult(stamp, current) {
			recorded = true
			changed = current.Quota.ObserveResponseHeadersForProvider(auth.Provider, headers, at)
		}
		m.mu.Unlock()
		release()
		if recorded {
			responseEndOnce.Do(func() {
				// AfterFunc registers no cancellation callback when Done is nil, so Background callers receive no response-end stamp.
				context.AfterFunc(ctx, func() { m.markResponseEnded(auth.ID) })
			})
		}
		if changed {
			m.quotaListeners.notify()
		}
	})
}
