package claudeusage

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// PlanCheckInterval is how often credentials are checked for a stale plan.
	PlanCheckInterval = time.Hour
	// PlanMaxAge bounds how long an upgrade or downgrade goes unnoticed; plans change rarely.
	PlanMaxAge = 6 * time.Hour

	// Auth metadata keys holding the last profile lookup, so it survives restarts.
	planTierKey      = "rate_limit_tier"
	planTypeKey      = "organization_type"
	planCheckedAtKey = "plan_checked_at"

	// claudeProType is the profile's organization_type for Pro subscriptions.
	claudeProType = "claude_pro"
)

// claudeTierMultiple reads the usage multiple from tiers like "default_claude_max_5x".
var claudeTierMultiple = regexp.MustCompile(`_(\d+)x$`)

// Plan is a Claude credential's subscription as of its last profile lookup.
type Plan struct {
	// Tier is the profile's organization.rate_limit_tier, e.g. "default_claude_max_5x".
	Tier string
	// Type is the profile's organization.organization_type, e.g. "claude_max".
	Type string
	// Label is a short name, e.g. "Max 5x" or "Pro".
	Label string
	// Multiplier is usage per 5-hour session relative to Pro (Max 5x = 5); zero when unknown.
	Multiplier float64
}

// PlanOf returns the credential's last looked-up plan; false before the first lookup.
func PlanOf(auth *coreauth.Auth) (Plan, bool) {
	if auth == nil {
		return Plan{}, false
	}
	tier := metadataString(auth, planTierKey)
	planType := metadataString(auth, planTypeKey)
	if tier == "" && planType == "" {
		return Plan{}, false
	}
	plan := Plan{Tier: tier, Type: planType, Label: tier}
	if match := claudeTierMultiple.FindStringSubmatch(tier); match != nil {
		if multiple, errParse := strconv.Atoi(match[1]); errParse == nil && multiple > 0 {
			plan.Multiplier = float64(multiple)
			plan.Label = fmt.Sprintf("Max %dx", multiple)
		}
	} else if planType == claudeProType {
		plan.Multiplier = 1
		plan.Label = "Pro"
	}
	if plan.Label == "" {
		plan.Label = planType
	}
	return plan, true
}

func metadataString(auth *coreauth.Auth, key string) string {
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

// ProfileFunc returns the OAuth profile for one credential.
type ProfileFunc func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*claudeauth.OAuthProfile, error)

// PlanTracker keeps Claude credentials' subscription plans current.
type PlanTracker struct {
	manager *coreauth.Manager
	config  func() *config.Config
	fetch   ProfileFunc
	now     func() time.Time
	spacing time.Duration

	mu      sync.Mutex
	backoff map[string]time.Time
}

// NewPlanTracker builds a tracker over manager. cfg supplies the live config.
func NewPlanTracker(manager *coreauth.Manager, cfg func() *config.Config) *PlanTracker {
	return &PlanTracker{manager: manager, config: cfg, fetch: fetchProfile, now: time.Now, spacing: requestSpacing, backoff: map[string]time.Time{}}
}

// Run refreshes stale plans immediately and then every PlanCheckInterval until ctx ends.
func (t *PlanTracker) Run(ctx context.Context) {
	for {
		t.RefreshStale(ctx)
		timer := time.NewTimer(PlanCheckInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// RefreshStale looks up every eligible credential whose plan is missing or older than PlanMaxAge.
func (t *PlanTracker) RefreshStale(ctx context.Context) {
	for _, auth := range t.manager.List() {
		if ctx.Err() != nil {
			return
		}
		if !Eligible(auth) || !t.isStale(auth) || t.inBackoff(auth.ID) {
			continue
		}
		if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(t.now()) {
			continue // the auth manager renews it; the next check retries
		}
		if errRefresh := t.Refresh(ctx, auth); errRefresh != nil {
			log.Debugf("claude plan lookup for %s failed: %v", auth.ID, errRefresh)
		}
		t.wait(ctx)
	}
}

// Refresh looks up one credential's plan now and stores it on the credential.
func (t *PlanTracker) Refresh(ctx context.Context, auth *coreauth.Auth) error {
	lookupCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	profile, errFetch := t.fetch(lookupCtx, t.config(), auth)
	if errFetch != nil {
		// A credential without the profile scope fails every time; retry at the plan cadence.
		t.setBackoff(auth.ID, t.now().Add(PlanMaxAge))
		return errFetch
	}
	checkedAt := t.now().UTC().Format(time.RFC3339)
	_, _, errModify := t.manager.Modify(ctx, auth.ID, 0, func(current *coreauth.Auth) error {
		if current.Metadata == nil {
			current.Metadata = map[string]any{}
		}
		current.Metadata[planTierKey] = strings.TrimSpace(profile.Organization.RateLimitTier)
		current.Metadata[planTypeKey] = strings.TrimSpace(profile.Organization.OrganizationType)
		current.Metadata[planCheckedAtKey] = checkedAt
		return nil
	})
	if errModify == nil {
		t.setBackoff(auth.ID, time.Time{})
	}
	return errModify
}

func (t *PlanTracker) isStale(auth *coreauth.Auth) bool {
	checkedAt, errParse := time.Parse(time.RFC3339, metadataString(auth, planCheckedAtKey))
	return errParse != nil || t.now().Sub(checkedAt) > PlanMaxAge
}

func (t *PlanTracker) inBackoff(authID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.backoff[authID]
	return ok && t.now().Before(until)
}

func (t *PlanTracker) setBackoff(authID string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if until.IsZero() {
		delete(t.backoff, authID)
		return
	}
	t.backoff[authID] = until
}

func (t *PlanTracker) wait(ctx context.Context) {
	if t.spacing <= 0 {
		return
	}
	timer := time.NewTimer(t.spacing)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func fetchProfile(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*claudeauth.OAuthProfile, error) {
	return claudeauth.NewClaudeAuthWithProxyURL(cfg, auth.ProxyURL).FetchOAuthProfile(ctx, accessToken(auth))
}
