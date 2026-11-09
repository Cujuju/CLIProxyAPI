// Package resetcredits keeps an account's rate-limit reset credits in its auth metadata, for every
// provider that grants them (Codex, Claude), and names the redeem outcomes the desktop API reports.
package resetcredits

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Auth metadata keys holding the last lookup, so it survives restarts.
const (
	availableKey  = "reset_credits_available"
	applicableKey = "reset_credits_applicable"
	checkedAtKey  = "reset_credits_checked_at"
	expiriesKey   = "reset_credits_expiries"
)

// Redeem outcome codes, as POST /usage/reset-credit reports them.
const (
	// Reset means a credit was spent; what it cleared shows in the next usage lookup.
	Reset = "reset"
	// NothingToReset means no limit needed clearing, so no credit was spent.
	NothingToReset = "nothing_to_reset"
	// NoCredit means the account holds no usable credit.
	NoCredit = "no_credit"
	// AlreadyRedeemed means this redeem request was used before.
	AlreadyRedeemed = "already_redeemed"
)

// Credits are an account's rate-limit reset credits as of its last lookup.
type Credits struct {
	// Available is how many credits the account holds.
	Available int
	// Applicable is how many can be used now.
	Applicable int
	CheckedAt  time.Time
	// Expiries are the held credits' expiry times, soonest first. A credit that never expires, or one
	// whose expiry lookup failed, has none, so there may be fewer than Available.
	Expiries []time.Time
}

// Same reports equal counts and expiries, ignoring when they were checked.
func (c Credits) Same(other Credits) bool {
	return c.Available == other.Available && c.Applicable == other.Applicable &&
		slices.EqualFunc(c.Expiries, other.Expiries, time.Time.Equal)
}

// Of returns the credential's last looked-up reset credits; false when never reported.
func Of(auth *coreauth.Auth) (Credits, bool) {
	if auth == nil || auth.Metadata == nil {
		return Credits{}, false
	}
	available, okAvailable := metadataInt(auth.Metadata[availableKey])
	applicable, okApplicable := metadataInt(auth.Metadata[applicableKey])
	checkedText, _ := auth.Metadata[checkedAtKey].(string)
	checkedAt, errParse := time.Parse(time.RFC3339, strings.TrimSpace(checkedText))
	if !okAvailable || !okApplicable || errParse != nil {
		return Credits{}, false
	}
	return Credits{Available: available, Applicable: applicable, CheckedAt: checkedAt, Expiries: metadataTimes(auth.Metadata[expiriesKey])}, true
}

// Store records a lookup made at checkedAt into metadata. Nil credits mean the account reports none,
// which clears the counts; credits.CheckedAt is ignored.
func Store(metadata map[string]any, checkedAt time.Time, credits *Credits) {
	metadata[checkedAtKey] = checkedAt.UTC().Format(time.RFC3339)
	if credits == nil {
		delete(metadata, availableKey)
		delete(metadata, applicableKey)
		delete(metadata, expiriesKey)
		return
	}
	metadata[availableKey] = credits.Available
	metadata[applicableKey] = credits.Applicable
	if len(credits.Expiries) == 0 {
		delete(metadata, expiriesKey)
		return
	}
	expiries := make([]string, len(credits.Expiries))
	for i, at := range credits.Expiries {
		expiries[i] = at.UTC().Format(time.RFC3339)
	}
	metadata[expiriesKey] = expiries
}

// metadataTimes reads RFC 3339 times stored as []string, or as []any after a JSON round trip.
func metadataTimes(value any) []time.Time {
	var raw []string
	switch v := value.(type) {
	case []string:
		raw = v
	case []any:
		for _, item := range v {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	}
	var out []time.Time
	for _, text := range raw {
		if at, errParse := time.Parse(time.RFC3339, text); errParse == nil {
			out = append(out, at)
		}
	}
	return out
}

// metadataInt reads a count stored as an int, or as a float64 after a JSON round trip.
func metadataInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	case json.Number:
		n, errInt := v.Int64()
		return int(n), errInt == nil
	}
	return 0, false
}
