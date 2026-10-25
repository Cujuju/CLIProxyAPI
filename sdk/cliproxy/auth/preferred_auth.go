package auth

import (
	"context"
	"strings"
	"sync"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// preferredAuths is the desktop "Use account" override: per provider, the credential to pick while it
// is available. It outranks the routing strategy and session affinity; when the credential is cooling
// down, disabled or already tried, selection falls back to automatic routing. It lives in memory only;
// the supervisor re-applies it after a restart.
type preferredAuths struct {
	mu         sync.RWMutex
	byProvider map[string]string
}

// SetPreferredAuth prefers authID for provider; an empty authID restores automatic routing.
func (m *Manager) SetPreferredAuth(provider, authID string) {
	provider = canonicalSchedulingProvider(provider)
	authID = strings.TrimSpace(authID)
	m.preferred.mu.Lock()
	defer m.preferred.mu.Unlock()
	if authID == "" {
		delete(m.preferred.byProvider, provider)
		return
	}
	if m.preferred.byProvider == nil {
		m.preferred.byProvider = map[string]string{}
	}
	m.preferred.byProvider[provider] = authID
}

// PreferredAuths returns provider → preferred credential ID.
func (m *Manager) PreferredAuths() map[string]string {
	m.preferred.mu.RLock()
	defer m.preferred.mu.RUnlock()
	out := make(map[string]string, len(m.preferred.byProvider))
	for provider, id := range m.preferred.byProvider {
		out[provider] = id
	}
	return out
}

// pickPreferred picks a provider's preferred credential when one is set, untried and available.
// A request that already pins a credential keeps its own pin.
func (m *Manager) pickPreferred(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, bool) {
	if pinnedAuthIDFromMetadata(opts.Metadata) != "" {
		return nil, nil, "", false
	}
	preferred := m.PreferredAuths()
	if len(preferred) == 0 {
		return nil, nil, "", false
	}
	for _, provider := range providers {
		authID := preferred[canonicalSchedulingProvider(provider)]
		if authID == "" {
			continue
		}
		if _, done := tried[authID]; done {
			continue
		}
		pinnedOpts := opts
		pinnedOpts.Metadata = cloneRequestMetadata(opts.Metadata)
		pinnedOpts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = authID
		auth, executor, pickedProvider, err := m.pickNextMixedCandidate(ctx, []string{provider}, model, pinnedOpts, tried)
		if err == nil && auth != nil {
			return auth, executor, pickedProvider, true
		}
	}
	return nil, nil, "", false
}
