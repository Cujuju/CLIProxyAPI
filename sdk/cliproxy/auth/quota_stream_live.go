package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// observeStreamQuota publishes watermarks when received, independently of stream completion.
func (m *Manager) observeStreamQuota(ctx context.Context, auth *Auth, optsHeaders http.Header) context.Context {
	if m.HomeEnabled() || auth == nil || !ProviderSupportsQuotaObservation(auth.Provider) {
		return ctx
	}
	stamp := Result{AuthID: auth.ID, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, AccessToken: authAccessTokenForRequest(ctx, auth, optsHeaders)}
	return logging.WithResponseHeadersObserver(ctx, func(headers http.Header, at time.Time) {
		release := m.lockAuthMutation(auth.ID)
		m.mu.Lock()
		current := m.auths[auth.ID]
		changed := false
		if current != nil && !isStaleExecutionResult(stamp, current) {
			changed = current.Quota.ObserveResponseHeadersForProvider(auth.Provider, headers, at)
		}
		m.mu.Unlock()
		release()
		if changed {
			m.quotaListeners.notify()
		}
	})
}
