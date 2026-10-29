package auth

import (
	"maps"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/tidwall/gjson"
)

// Moving a conversation off its spent credential re-sends the whole context without the prompt
// cache. Waiting for that credential's reset only saves it while the cache outlives the wait, so a
// session is held only when the reset comes before the cache it declared expires.

// defaultPromptCacheLifetime is Anthropic's ephemeral cache lifetime when cache_control has no ttl.
const defaultPromptCacheLifetime = 5 * time.Minute

// affinityProvider is the provider namespace mixed-provider selection binds sessions under.
const affinityProvider = "mixed"

// servedSessions records when each bound session last got a successful response, which is when its
// prompt cache was last used.
type servedSessions struct {
	mu sync.Mutex
	at map[string]time.Time
}

// record notes a success and drops sessions idle longer than ttl, whose bindings have expired.
func (s *servedSessions) record(key string, now time.Time, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at == nil {
		s.at = make(map[string]time.Time)
	}
	for other, servedAt := range s.at {
		if now.Sub(servedAt) > ttl {
			delete(s.at, other)
		}
	}
	s.at[key] = now
}

func (s *servedSessions) lastServed(key string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at[key]
}

// heldUsageLimit reports the bound credential's reset when the request's session should wait for it.
func (s *SessionAffinitySelector) heldUsageLimit(opts cliproxyexecutor.Options, model string, auths []*Auth, now time.Time) (quotaLimit, bool) {
	if s == nil || s.hold <= 0 {
		return quotaLimit{}, false
	}
	// Extraction records fork and parent markers in metadata; the pick that follows does that itself.
	metadata := maps.Clone(opts.Metadata)
	sessionID, _ := extractSessionIDs(opts.Headers, opts.OriginalRequest, metadata)
	if sessionID == "" {
		return quotaLimit{}, false
	}
	key := affinityProvider + "::" + cliproxysession.BoundSessionIdentity(sessionID) + "::" + canonicalModelKey(model)
	authID, bindingExpiresAt, bound := s.cache.peek(key)
	servedAt := s.served.lastServed(key)
	if !bound || servedAt.IsZero() {
		return quotaLimit{}, false
	}
	var boundAuth *Auth
	for _, auth := range auths {
		if auth != nil && auth.ID == authID {
			boundAuth = auth
			break
		}
	}
	if boundAuth == nil || boundAuth.Disabled {
		return quotaLimit{}, false
	}
	limit, exhausted := credentialQuotaLimit(boundAuth, model, now)
	if !exhausted || !limit.ResetsAt.After(now) || limit.ResetsAt.After(now.Add(s.hold)) {
		return quotaLimit{}, false
	}
	cacheExpiresAt := servedAt.Add(declaredPromptCacheLifetime(opts.OriginalRequest))
	// The binding must also survive until the reset, or the resumed request starts elsewhere.
	if limit.ResetsAt.After(cacheExpiresAt) || !limit.ResetsAt.Before(bindingExpiresAt) {
		return quotaLimit{}, false
	}
	return limit, true
}

// declaredPromptCacheLifetime is the longest prompt cache a Claude request asks for; zero when it
// asks for none (including non-Claude bodies, which declare no lifetime).
func declaredPromptCacheLifetime(body []byte) time.Duration {
	var longest time.Duration
	note := func(control gjson.Result) {
		if !control.Exists() {
			return
		}
		lifetime := defaultPromptCacheLifetime
		if ttl, errParse := time.ParseDuration(control.Get("ttl").String()); errParse == nil && ttl > 0 {
			lifetime = ttl
		}
		longest = max(longest, lifetime)
	}
	eachCacheControl := func(list gjson.Result) {
		list.ForEach(func(_, item gjson.Result) bool {
			note(item.Get("cache_control"))
			return true
		})
	}
	root := gjson.ParseBytes(body)
	note(root.Get("cache_control"))
	eachCacheControl(root.Get("system"))
	eachCacheControl(root.Get("tools"))
	root.Get("messages").ForEach(func(_, message gjson.Result) bool {
		eachCacheControl(message.Get("content"))
		return true
	})
	return longest
}

// sessionHoldLimit answers a held session with its bound credential's usage limit.
func (m *Manager) sessionHoldLimit(opts cliproxyexecutor.Options, providers []string, model string) error {
	affinity, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || affinity.hold <= 0 || model == "" {
		return nil
	}
	wanted := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		wanted[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	var auths []*Auth
	for _, auth := range m.snapshotAuths() {
		if auth == nil {
			continue
		}
		if _, ok := wanted[canonicalSchedulingProvider(executorKeyFromAuth(auth))]; ok {
			auths = append(auths, auth)
		}
	}
	now := time.Now()
	limit, held := affinity.heldUsageLimit(opts, model, auths, now)
	if !held {
		return nil
	}
	usageLimit := newModelCooldownError(model, "", limit.ResetsAt.Sub(now))
	usageLimit.usageLimit = limit
	return usageLimit
}
