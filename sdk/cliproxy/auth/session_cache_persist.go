package auth

import (
	"sort"
	"time"
)

// SessionBindingRecord is one session-affinity binding as persisted across restarts.
type SessionBindingRecord struct {
	AuthID    string    `json:"auth_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Aliases   []string  `json:"aliases"`
	SessionID string    `json:"session_id,omitempty"`
	Provider  string    `json:"provider,omitempty"`
}

// Snapshot returns the live bindings, oldest selection first.
func (c *SessionCache) Snapshot() []SessionBindingRecord {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	groups := make([]sessionEntry, 0, len(c.groups))
	for _, group := range c.groups {
		if now.Before(group.expiresAt) {
			groups = append(groups, group)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].binding.sequence != groups[j].binding.sequence {
			return groups[i].binding.sequence < groups[j].binding.sequence
		}
		return groups[i].aliases[0] < groups[j].aliases[0]
	})
	records := make([]SessionBindingRecord, 0, len(groups))
	for _, group := range groups {
		records = append(records, SessionBindingRecord{
			AuthID:    group.authID,
			ExpiresAt: group.expiresAt,
			Aliases:   append([]string(nil), group.aliases...),
			SessionID: group.binding.sessionID,
			Provider:  group.binding.provider,
		})
	}
	return records
}

// Restore adds persisted bindings in order, keeping their expiry. Expired records and
// records whose aliases are already bound (a request arrived first) are skipped.
func (c *SessionCache) Restore(records []SessionBindingRecord) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	now := time.Now()
	restored := 0
	for _, record := range records {
		aliases := compactSessionAliases(mergeSessionAliases(nil, record.Aliases...))
		if record.AuthID == "" || len(aliases) == 0 || !now.Before(record.ExpiresAt) || c.anyAliasBoundLocked(aliases, now) {
			continue
		}
		binding := sessionBinding{sessionID: record.SessionID, provider: record.Provider}
		if binding.sessionID != "" && binding.provider != "" {
			c.bindingSequence++
			binding.sequence = c.bindingSequence
		}
		c.replaceAliasGroupsLocked(record.AuthID, record.ExpiresAt, aliases, binding)
		restored++
	}
	return restored
}

func (c *SessionCache) anyAliasBoundLocked(aliases []string, now time.Time) bool {
	for _, alias := range aliases {
		if entry, ok := c.entries[alias]; ok && now.Before(entry.expiresAt) {
			return true
		}
	}
	return false
}
