package cliproxy

import (
	"context"
	"strings"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// antigravityModelRefreshRequest captures publication ownership at admission.
// A queued successor must not adopt a later epoch from an external publisher.
type antigravityModelRefreshRequest struct {
	ctx                context.Context
	auth               *coreauth.Auth
	provider           string
	registryEpoch      uint64
	routeKey           string
	token              string
	minCatalogRevision uint64
	fallback           *antigravitySummaryFallback
}

type antigravitySummaryFallback struct {
	ctx                context.Context
	probe              sdkauth.AntigravityModelProbeRequest
	summary            antigravityModelCapabilityHints
	minCatalogRevision uint64
}

func (r *antigravityModelRefreshRequest) accountProbe() *antigravityAccountProbe {
	return &antigravityAccountProbe{auth: r.auth, routeKey: r.routeKey, token: r.token}
}

type antigravityHintPersistRequest struct {
	ctx      context.Context
	auth     *coreauth.Auth
	routeKey string
	endpoint string
	hints    antigravityModelCapabilityHints
	cancel   context.CancelFunc
}

// queueAntigravityModelRefresh coalesces registration and periodic probes into
// one worker per account. Repeated updates replace a single queued successor,
// rather than accumulating singleflight followers while an upstream is stalled.
func (s *Service) queueAntigravityModelRefresh(ctx context.Context, auth *coreauth.Auth, provider string) {
	if s != nil {
		s.queueAntigravityRefresh(ctx, auth, provider, nil, &s.antigravityRefreshPending, s.executeAntigravityModelRefresh)
	}
}

func (s *Service) queueAntigravitySummaryRefresh(ctx context.Context, auth *coreauth.Auth) {
	if s != nil && s.coreManager != nil && s.NewAntigravityUsageLookup().Eligible(auth) {
		s.queueAntigravityRefresh(ctx, auth, "", nil, &s.antigravitySummaryPending, s.executeAntigravitySummaryRefresh)
	}
}

func (s *Service) queueAntigravityRefresh(ctx context.Context, auth *coreauth.Auth, provider string, fallback *antigravitySummaryFallback, pending *map[string]*antigravityModelRefreshRequest, execute func(context.Context, *antigravityModelRefreshRequest)) {
	if s == nil || auth == nil || auth.ID == "" || isAntigravityAuthDisabled(auth) || s.antigravityHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	if lifetime != nil && lifetime.Err() != nil {
		return
	}
	request := &antigravityModelRefreshRequest{
		ctx: ctx, auth: auth.Clone(), provider: provider,
		registryEpoch: GlobalModelRegistry().ClientRegistrationEpoch(auth.ID),
		routeKey:      s.antigravityCapabilityKey(auth), fallback: fallback,
	}
	token, _ := request.auth.Metadata["access_token"].(string)
	request.token = strings.TrimSpace(token)
	if pending == &s.antigravitySummaryPending {
		// A catalog completed after admission can satisfy this summary's fallback.
		antigravityCapabilityMu.RLock()
		request.minCatalogRevision = antigravityCapabilityCache[request.routeKey].hints.revision + 1
		antigravityCapabilityMu.RUnlock()
	}
	if fallback != nil && (request.token != fallback.probe.AccessToken ||
		request.routeKey != antigravityCapabilityKeyForRoute(auth, fallback.probe.BaseURLs, fallback.probe.ProxyURL, fallback.probe.UserAgent)) {
		return
	}
	s.antigravityProbeMu.Lock()
	// Late stale callers must not replace the latest queued auth snapshot.
	if ctx.Err() != nil || (lifetime != nil && lifetime.Err() != nil) || !s.antigravityAccountProbeCurrent(request.accountProbe()) {
		s.antigravityProbeMu.Unlock()
		return
	}
	if *pending == nil {
		*pending = make(map[string]*antigravityModelRefreshRequest)
	}
	queued, running := (*pending)[auth.ID]
	if queued != nil && (queued.ctx.Err() == nil || queued.fallback != nil) && s.antigravityAccountProbeCurrent(queued.accountProbe()) {
		// Preserve the newest registration and its publication epoch when coalescing fallback.
		if queued.provider != "" && (provider == "" || queued.registryEpoch > request.registryEpoch) {
			request = queued
		} else if queued.fallback != nil {
			request.fallback = queued.fallback
		}
		if fallback != nil && (request.fallback == nil ||
			fallback.summary.lookedUpAt.After(request.fallback.summary.lookedUpAt) ||
			(fallback.summary.lookedUpAt.Equal(request.fallback.summary.lookedUpAt) && fallback.summary.revision > request.fallback.summary.revision)) {
			request.fallback = fallback
		}
	}
	(*pending)[auth.ID] = request
	if !running {
		s.antigravityProbeWg.Add(1)
	}
	s.antigravityProbeMu.Unlock()
	if !running {
		go s.runAntigravityAccountRefresh(auth.ID, pending, execute)
	}
}

func (s *Service) runAntigravityAccountRefresh(authID string, pending *map[string]*antigravityModelRefreshRequest, execute func(context.Context, *antigravityModelRefreshRequest)) {
	defer s.antigravityProbeWg.Done()
	defer s.signalAntigravityModelRefresh()
	for {
		s.antigravityProbeMu.Lock()
		request := (*pending)[authID]
		if request == nil {
			delete(*pending, authID)
			s.antigravityProbeMu.Unlock()
			return
		}
		(*pending)[authID] = nil
		s.antigravityProbeMu.Unlock()
		s.executeAntigravityRefresh(request, execute)
		if request.fallback != nil {
			s.recordAntigravityBackgroundFallback(request)
		}
	}
}

func (s *Service) executeAntigravityRefresh(request *antigravityModelRefreshRequest, execute func(context.Context, *antigravityModelRefreshRequest)) {
	if request.ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, request.auth.Provider) {
		return
	}
	// Track queued workers even when another caller owns their singleflight probe.
	ctx, finish, active := s.beginAntigravityAccountProbe(request.ctx, request.auth, request.routeKey, "", request.token)
	if !active {
		return
	}
	defer finish()
	execute(ctx, request)
}

func (s *Service) executeAntigravityModelRefresh(ctx context.Context, request *antigravityModelRefreshRequest) {
	if request.provider != "" {
		expectedKey, hints := s.probeAndRegisterAntigravityModels(ctx, request.auth, request.provider, request.auth.RegistrationEpoch, request.registryEpoch)
		if hints.ModelIDs != nil {
			s.queueAntigravityHintPersistence(request.ctx, request.auth, expectedKey, antigravityModelBaseURLs(request.auth)[0], hints)
		}
	}
}

// antigravityAccountProbe owns both queued and connected work for one auth snapshot.
// Cancellation releases network slots without imposing an upstream timeout.
type antigravityAccountProbe struct {
	auth      *coreauth.Auth
	routeKey  string
	flightKey string
	token     string
	cancel    context.CancelFunc
}

func (s *Service) beginAntigravityAccountProbe(parent context.Context, auth *coreauth.Auth, routeKey, flightKey, token string) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancel(parent)
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	var stop func() bool
	if lifetime != nil {
		stop = context.AfterFunc(lifetime, cancel)
		if lifetime.Err() != nil {
			cancel()
		}
	}
	var changes <-chan struct{}
	var unsubscribe func()
	if s.coreManager != nil {
		// Subscribe before validation, so a native refresh/removal cannot fall
		// between checking the auth and installing the invalidation observer.
		changes, unsubscribe = s.coreManager.WatchAuthChanges(auth.ID)
	}
	var watcherDone chan struct{}
	probe := &antigravityAccountProbe{auth: auth.Clone(), routeKey: routeKey, flightKey: flightKey, token: token, cancel: cancel}
	s.antigravityProbeMu.Lock()
	valid := ctx.Err() == nil && s.antigravityAccountProbeCurrent(probe)
	if valid {
		if s.antigravityAccountProbes == nil {
			s.antigravityAccountProbes = make(map[string]map[*antigravityAccountProbe]struct{})
		}
		if s.antigravityAccountProbes[auth.ID] == nil {
			s.antigravityAccountProbes[auth.ID] = make(map[*antigravityAccountProbe]struct{})
		}
		s.antigravityAccountProbes[auth.ID][probe] = struct{}{}
	}
	s.antigravityProbeMu.Unlock()
	if valid && changes != nil {
		watcherDone = make(chan struct{})
		go func() {
			defer close(watcherDone)
			for {
				select {
				case <-ctx.Done():
					return
				case _, open := <-changes:
					if !open {
						return
					}
					s.cancelStaleAntigravityProbes(auth.ID)
				}
			}
		}()
	}
	cleanup := func() {
		cancel()
		if unsubscribe != nil {
			unsubscribe()
		}
		if watcherDone != nil {
			<-watcherDone
		}
		if stop != nil {
			stop()
		}
		s.antigravityProbeMu.Lock()
		delete(s.antigravityAccountProbes[auth.ID], probe)
		if len(s.antigravityAccountProbes[auth.ID]) == 0 {
			delete(s.antigravityAccountProbes, auth.ID)
		}
		s.antigravityProbeMu.Unlock()
		s.signalAntigravityModelRefresh()
	}
	if !valid {
		cleanup()
		return ctx, func() {}, false
	}
	return ctx, cleanup, true
}

// Caller holds antigravityProbeMu, making tracking and invalidation indivisible.
func (s *Service) antigravityAccountProbeCurrent(probe *antigravityAccountProbe) bool {
	if s.antigravityHomeEnabled() {
		return false
	}
	current := probe.auth
	if s.coreManager != nil {
		var exists bool
		current, exists = s.coreManager.GetByID(probe.auth.ID)
		if !exists || current == nil {
			return false
		}
	}
	token, _ := current.Metadata["access_token"].(string)
	return !current.Disabled && current.Status != coreauth.StatusDisabled &&
		antigravityAuthModelSettingsEqual(current, probe.auth) &&
		s.antigravityCapabilityKey(current) == probe.routeKey && strings.TrimSpace(token) == probe.token
}

// Run after the manager/config mutation. New probes check current state while
// joining this same map, so deletion cannot slip between validation and tracking.
// Empty authID invalidates stale probes across all accounts after config changes.
func (s *Service) queueAntigravityHintPersistence(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, hints antigravityModelCapabilityHints) {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" || isAntigravityAuthDisabled(auth) || expectedKey == "" || hints.ModelIDs == nil || hints.revision == 0 || s.antigravityHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	if lifetime != nil && lifetime.Err() != nil {
		return
	}
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[expectedKey]
	stale := entry.hints.ModelIDs == nil || entry.hints.revision != hints.revision || entry.persistedRevision >= hints.revision
	antigravityCapabilityMu.RUnlock()
	if stale {
		return
	}
	request := &antigravityHintPersistRequest{
		ctx:      ctx,
		auth:     auth.Clone(),
		routeKey: expectedKey,
		endpoint: endpoint,
		hints:    hints.clone(),
	}
	s.antigravityProbeMu.Lock()
	if ctx.Err() != nil || !s.antigravityHintPersistCurrent(request) {
		s.antigravityProbeMu.Unlock()
		return
	}
	if s.antigravityPersistPending == nil {
		s.antigravityPersistPending = make(map[string]*antigravityHintPersistRequest)
	}
	queued, running := s.antigravityPersistPending[auth.ID]
	if queued != nil && queued.hints.revision > request.hints.revision {
		s.antigravityProbeMu.Unlock()
		return
	}
	if active := s.antigravityPersistActive[auth.ID]; active != nil && (active.hints.revision < request.hints.revision || active.routeKey != request.routeKey) {
		if active.cancel != nil {
			active.cancel()
		}
	}
	s.antigravityPersistPending[auth.ID] = request
	if !running {
		s.antigravityProbeWg.Add(1)
	}
	s.antigravityProbeMu.Unlock()
	if !running {
		go s.runAntigravityHintPersistence(auth.ID)
	}
}

func (s *Service) runAntigravityHintPersistence(authID string) {
	defer s.antigravityProbeWg.Done()
	defer s.signalAntigravityModelRefresh()
	for {
		s.antigravityProbeMu.Lock()
		request := s.antigravityPersistPending[authID]
		if request == nil {
			delete(s.antigravityPersistPending, authID)
			delete(s.antigravityPersistActive, authID)
			s.antigravityProbeMu.Unlock()
			return
		}
		s.antigravityPersistPending[authID] = nil
		persistCtx, cancel := context.WithCancel(request.ctx)
		request.cancel = cancel
		if s.antigravityPersistActive == nil {
			s.antigravityPersistActive = make(map[string]*antigravityHintPersistRequest)
		}
		s.antigravityPersistActive[authID] = request
		s.antigravityProbeMu.Unlock()
		s.executeAntigravityHintPersistence(persistCtx, cancel, request)
	}
}

func (s *Service) executeAntigravityHintPersistence(persistCtx context.Context, cancel context.CancelFunc, request *antigravityHintPersistRequest) {
	defer cancel()
	if persistCtx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, request.auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	if lifetime != nil {
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if lifetime.Err() != nil {
			return
		}
	}
	if s.persistAntigravityCapabilityHints(persistCtx, request.auth, request.routeKey, request.endpoint, request.hints) && request.ctx.Err() == nil {
		s.queueAntigravityHintPersistence(request.ctx, request.auth, request.routeKey, request.endpoint, request.hints)
	}
}

func (s *Service) antigravityHintPersistCurrent(request *antigravityHintPersistRequest) bool {
	if request == nil || request.auth == nil || s.antigravityHomeEnabled() {
		return false
	}
	current := request.auth
	if s.coreManager != nil {
		var exists bool
		current, exists = s.coreManager.GetByID(request.auth.ID)
		if !exists || current == nil {
			return false
		}
	}
	return !current.Disabled && current.Status != coreauth.StatusDisabled &&
		current.Provider == request.auth.Provider &&
		current.RegistrationEpoch == request.auth.RegistrationEpoch &&
		s.antigravityCapabilityKey(current) == request.routeKey
}

func (s *Service) cancelStaleAntigravityProbes(authID string) {
	if s == nil {
		return
	}
	s.antigravityProbeMu.Lock()
	for _, pending := range []map[string]*antigravityModelRefreshRequest{s.antigravityRefreshPending, s.antigravitySummaryPending} {
		for id, request := range pending {
			if (authID == "" || id == authID) && request != nil && !s.antigravityAccountProbeCurrent(request.accountProbe()) {
				pending[id] = nil
			}
		}
	}
	for id, probes := range s.antigravityAccountProbes {
		if authID != "" && id != authID {
			continue
		}
		for probe := range probes {
			if !s.antigravityAccountProbeCurrent(probe) {
				probe.cancel()
				// An updated snapshot with the same token may start immediately instead
				// of joining an obsolete leader that is still unwinding its HTTP request.
				if probe.flightKey != "" {
					antigravityCapabilityGroup.Forget(probe.flightKey)
				}
				delete(probes, probe)
			}
		}
		if len(probes) == 0 {
			delete(s.antigravityAccountProbes, id)
		}
	}
	for id, active := range s.antigravityPersistActive {
		if authID != "" && id != authID {
			continue
		}
		if active != nil && !s.antigravityHintPersistCurrent(active) {
			if active.cancel != nil {
				active.cancel()
			}
		}
	}
	for id, pending := range s.antigravityPersistPending {
		if authID != "" && id != authID {
			continue
		}
		if pending != nil && !s.antigravityHintPersistCurrent(pending) {
			s.antigravityPersistPending[id] = nil
		}
	}
	s.antigravityProbeMu.Unlock()
	s.signalAntigravityModelRefresh()
}

func (s *Service) antigravityModelRefreshWake() chan struct{} {
	s.antigravityProbeMu.Lock()
	defer s.antigravityProbeMu.Unlock()
	if s.antigravityRefreshWake == nil {
		s.antigravityRefreshWake = make(chan struct{}, 1)
	}
	return s.antigravityRefreshWake
}

func (s *Service) signalAntigravityModelRefresh() {
	if s == nil {
		return
	}
	select {
	case s.antigravityModelRefreshWake() <- struct{}{}:
	default:
	}
}
