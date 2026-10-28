package auth

import (
	"strings"
	"time"
)

// subagentSessionMarker joins a conversation's session ID to a subagent's own ID.
const subagentSessionMarker = ":agent:"

// MoveSession rebinds the live bindings of a conversation, and of its subagents, held by a credential
// in movable, to authID. It returns how many such bindings exist, including any already on authID.
func (c *SessionCache) MoveSession(sessionID, authID string, movable map[string]struct{}) int {
	if c == nil || sessionID == "" || authID == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var matched []sessionEntry
	for _, group := range c.groups {
		if _, ok := movable[group.authID]; ok && now.Before(group.expiresAt) && groupBindsSession(group.aliases, sessionID) {
			matched = append(matched, group)
		}
	}
	for _, group := range matched {
		if group.authID == authID {
			continue
		}
		binding := group.binding
		if binding.sequence != 0 {
			// The move is the conversation's latest selection.
			c.bindingSequence++
			binding.sequence = c.bindingSequence
		}
		c.replaceAliasGroupsLocked(authID, group.expiresAt, group.aliases, binding, group)
	}
	return len(matched)
}

// groupBindsSession reports whether an alias is "<namespace>::<session>[:agent:<id>]::<model>".
func groupBindsSession(aliases []string, sessionID string) bool {
	needle := "::" + sessionID
	for _, alias := range aliases {
		at := strings.Index(alias, needle)
		if at < 0 {
			continue
		}
		rest := alias[at+len(needle):]
		if strings.HasPrefix(rest, "::") || strings.HasPrefix(rest, subagentSessionMarker) {
			return true
		}
	}
	return false
}

// MoveSession rebinds a conversation, and its subagents, from credentials of target's provider to target.
// ok is false when session affinity is off.
func (m *Manager) MoveSession(sessionID string, target *Auth) (bound int, ok bool) {
	affinity, isAffinity := m.Selector().(*SessionAffinitySelector)
	if !isAffinity || affinity == nil || target == nil {
		return 0, false
	}
	// Built before the cache lock: result handling takes the manager lock, then the cache lock.
	provider := canonicalSchedulingProvider(executorKeyFromAuth(target))
	movable := map[string]struct{}{}
	for _, auth := range m.List() {
		if auth != nil && canonicalSchedulingProvider(executorKeyFromAuth(auth)) == provider {
			movable[auth.ID] = struct{}{}
		}
	}
	return affinity.cache.MoveSession(sessionID, target.ID, movable), true
}
