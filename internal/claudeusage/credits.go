package claudeusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RedeemFunc claims one reset from grantID and returns Claude's result.
type RedeemFunc func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, organizationUUID, grantID, requestID string) (string, error)

// usageCredits is the usage document's reset-credit block, requested with cedar_ember=1.
type usageCredits struct {
	CedarEmber *struct {
		Eligible      bool    `json:"eligible"`
		Grants        []grant `json:"grants"`
		NextGrantID   string  `json:"next_grant_id"`
		CooldownUntil string  `json:"cooldown_until"`
	} `json:"cedar_ember"`
}

// grant is one batch of resets; resets_left of them remain.
type grant struct {
	ID         string `json:"id"`
	ResetsLeft int    `json:"resets_left"`
	EndsAt     string `json:"ends_at"`
	Paused     bool   `json:"paused"`
	UsableNow  bool   `json:"usable_now"`
}

// usable reports a grant that can be claimed now.
func (g grant) usable() bool { return g.UsableNow && !g.Paused }

// endsAt is the grant's expiry; false when it never expires or the time is unreadable.
func (g grant) endsAt() (time.Time, bool) {
	at, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(g.EndsAt))
	return at.UTC(), errParse == nil
}

// creditState is a parsed reset-credit block.
type creditState struct {
	credits *resetcredits.Credits
	// nextGrant is the grant Claude spends next; empty when none is usable.
	nextGrant string
	// coolingUntil is set while Claude refuses claims.
	coolingUntil time.Time
}

// parseCredits reads the reset credits from a usage document. Credits are nil when the account is
// ineligible or the block is missing, so nothing is shown.
func parseCredits(raw json.RawMessage, now time.Time) creditState {
	var doc usageCredits
	if errDecode := json.Unmarshal(raw, &doc); errDecode != nil || doc.CedarEmber == nil || !doc.CedarEmber.Eligible {
		return creditState{}
	}
	block := doc.CedarEmber
	var state creditState
	if until, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(block.CooldownUntil)); errParse == nil && until.After(now) {
		state.coolingUntil = until
	}
	credits := &resetcredits.Credits{}
	for _, g := range block.Grants {
		endsAt, expires := g.endsAt()
		if g.ResetsLeft <= 0 || (expires && !endsAt.After(now)) {
			continue
		}
		credits.Available += g.ResetsLeft
		if g.usable() && state.coolingUntil.IsZero() {
			credits.Applicable += g.ResetsLeft
			if g.ID == block.NextGrantID {
				state.nextGrant = g.ID
			}
		}
		if expires {
			for range g.ResetsLeft {
				credits.Expiries = append(credits.Expiries, endsAt)
			}
		}
	}
	slices.SortFunc(credits.Expiries, func(a, b time.Time) int { return a.Compare(b) })
	state.credits = credits
	return state
}

// storeCredits records a lookup's reset credits. It skips the write when nothing changed and the
// stored check is younger than MaxLookupAge, so frequent lookups don't rewrite the auth file.
func (p *Poller) storeCredits(ctx context.Context, auth *coreauth.Auth, credits *resetcredits.Credits, lookedUpAt time.Time) {
	stored, had := resetcredits.Of(auth)
	if credits == nil && !had {
		return
	}
	if credits != nil && had && stored.Same(*credits) && lookedUpAt.Sub(stored.CheckedAt) < MaxLookupAge {
		return
	}
	_, _, errModify := p.manager.Modify(ctx, auth.ID, 0, func(current *coreauth.Auth) error {
		if current.Metadata == nil {
			current.Metadata = map[string]any{}
		}
		resetcredits.Store(current.Metadata, lookedUpAt, credits)
		return nil
	})
	if errModify != nil {
		log.Warnf("claude reset credits for auth_index=%s not stored: %v", auth.EnsureIndex(), errModify)
	}
}

// claimResults maps Claude's claim results to the redeem outcome codes.
var claimResults = map[string]string{
	"reset":        resetcredits.Reset,
	"not_limited":  resetcredits.NothingToReset,
	"already_used": resetcredits.AlreadyRedeemed,
	"ineligible":   resetcredits.NoCredit,
}

// RedeemResetCredit spends one of the account's reset credits. A fresh lookup picks Claude's next
// grant; each claim carries a new request id, so it spends at most one reset.
func (p *Poller) RedeemResetCredit(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if !Eligible(auth) {
		return "", fmt.Errorf("credential %s is not an enabled Claude OAuth credential", auth.ID)
	}
	organization, _ := auth.Metadata["organization_uuid"].(string)
	if strings.TrimSpace(organization) == "" {
		return "", errors.New("this Claude login has no organization; sign in to it again")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	raw, errFetch := p.fetch(lookupCtx, p.config(), auth)
	if errFetch != nil {
		return "", errFetch
	}
	state := parseCredits(raw, p.now())
	if !state.coolingUntil.IsZero() {
		return "", fmt.Errorf("Claude resets are cooling down until %s", state.coolingUntil.Format(time.RFC3339))
	}
	if state.nextGrant == "" {
		return resetcredits.NoCredit, nil
	}
	result, errRedeem := p.redeem(lookupCtx, p.config(), auth, organization, state.nextGrant, uuid.NewString())
	var httpErr *claudeauth.UsageHTTPError
	switch {
	case errors.As(errRedeem, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests:
		return "", errors.New("Claude is rate limiting resets; try again soon")
	case errors.As(errRedeem, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden):
		return "", errors.New("Claude refused the login; sign in to this account again")
	case errRedeem != nil:
		return "", errRedeem
	case result == "cooldown":
		return "", errors.New("Claude resets are cooling down; try again later")
	case result == "unavailable":
		return "", errors.New("Claude could not confirm the reset; check the limits before trying again")
	}
	code, known := claimResults[result]
	if !known {
		return "", fmt.Errorf("unexpected Claude reset result %q", result)
	}
	return code, nil
}

func redeemResetCredit(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, organizationUUID, grantID, requestID string) (string, error) {
	return claudeauth.NewClaudeAuthWithProxyURL(cfg, auth.ProxyURL).RedeemResetCredit(ctx, accessToken(auth), claudeCodeUserAgent(cfg, auth), organizationUUID, grantID, requestID)
}
