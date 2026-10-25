package claudeusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// CheckInterval is how often stale credentials are looked for; a newly
	// added credential gets its first lookup within this delay.
	CheckInterval = time.Minute
	// MaxObservationAge bounds staleness. Usage only moves with traffic and
	// proxied traffic refreshes the snapshot passively, so this mainly catches
	// use outside the proxy (claude.ai, other machines).
	MaxObservationAge = 15 * time.Minute
	// FailureBackoff stops a failing credential from being retried every tick.
	FailureBackoff = 15 * time.Minute
	// requestSpacing keeps sequential lookups from bursting.
	requestSpacing = 2 * time.Second
	// requestTimeout bounds one control-plane lookup (see AGENTS.md exceptions).
	requestTimeout = 20 * time.Second
)

// FetchFunc returns the raw usage document for one credential.
type FetchFunc func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (json.RawMessage, error)

// Poller keeps Claude credentials' quota snapshots fresh.
type Poller struct {
	manager *coreauth.Manager
	config  func() *config.Config
	fetch   FetchFunc
	now     func() time.Time
	spacing time.Duration

	mu      sync.Mutex
	backoff map[string]time.Time
}

// NewPoller builds a poller over manager. cfg supplies the live config.
func NewPoller(manager *coreauth.Manager, cfg func() *config.Config) *Poller {
	return &Poller{manager: manager, config: cfg, fetch: fetchUsage, now: time.Now, spacing: requestSpacing, backoff: map[string]time.Time{}}
}

// Run refreshes stale credentials immediately and then every CheckInterval
// until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()
	for {
		p.RefreshStale(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RefreshStale looks up every eligible credential whose snapshot is stale.
func (p *Poller) RefreshStale(ctx context.Context) {
	for _, auth := range p.manager.List() {
		if ctx.Err() != nil {
			return
		}
		if !Eligible(auth) || !p.isStale(auth) || p.inBackoff(auth.ID) {
			continue
		}
		if errRefresh := p.Refresh(ctx, auth); errRefresh != nil {
			log.Debugf("claude usage lookup for %s failed: %v", auth.ID, errRefresh)
		}
		p.wait(ctx)
	}
}

// RefreshAll looks up every eligible credential now, ignoring staleness and
// backoff. Errors are returned per credential ID.
func (p *Poller) RefreshAll(ctx context.Context) map[string]error {
	failures := map[string]error{}
	first := true
	for _, auth := range p.manager.List() {
		if !Eligible(auth) {
			continue
		}
		if !first {
			p.wait(ctx)
		}
		first = false
		if ctx.Err() != nil {
			return failures
		}
		if errRefresh := p.Refresh(ctx, auth); errRefresh != nil {
			failures[auth.ID] = errRefresh
		}
	}
	return failures
}

// Refresh looks up one credential now and records the snapshot.
func (p *Poller) Refresh(ctx context.Context, auth *coreauth.Auth) error {
	if !Eligible(auth) {
		return fmt.Errorf("credential %s is not an enabled Claude OAuth credential", auth.ID)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	raw, errFetch := p.fetch(lookupCtx, p.config(), auth)
	if errFetch == nil {
		headers, errConvert := HeadersFromUsage(raw)
		if errConvert == nil {
			p.manager.ObserveQuotaHeaders(auth.ID, headers, p.now())
			p.setBackoff(auth.ID, time.Time{})
			return nil
		}
		errFetch = errConvert
	}
	p.setBackoff(auth.ID, p.now().Add(FailureBackoff))
	return errFetch
}

// Eligible reports whether auth is an enabled Claude OAuth credential.
func Eligible(auth *coreauth.Auth) bool {
	if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return false
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != "" {
		return false
	}
	return accessToken(auth) != ""
}

func accessToken(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	token, _ := auth.Metadata["access_token"].(string)
	return strings.TrimSpace(token)
}

// isStale reports a missing or old snapshot, or an account window whose reset
// has passed (its next reset is unknown until looked up).
func (p *Poller) isStale(auth *coreauth.Auth) bool {
	now := p.now()
	if auth.Quota.ObservedAt.IsZero() || now.Sub(auth.Quota.ObservedAt) > MaxObservationAge {
		return true
	}
	for _, window := range coreauth.QuotaWindows(auth, now) {
		if !window.Scoped && !window.ResetsAt.IsZero() && !window.ResetsAt.After(now) {
			return true
		}
	}
	return false
}

func (p *Poller) inBackoff(authID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.backoff[authID]
	return ok && p.now().Before(until)
}

func (p *Poller) setBackoff(authID string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.IsZero() {
		delete(p.backoff, authID)
		return
	}
	p.backoff[authID] = until
}

func (p *Poller) wait(ctx context.Context) {
	if p.spacing <= 0 {
		return
	}
	timer := time.NewTimer(p.spacing)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func fetchUsage(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (json.RawMessage, error) {
	if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(time.Now()) {
		return nil, errors.New("access token expired; waiting for refresh")
	}
	return claudeauth.NewClaudeAuthWithProxyURL(cfg, auth.ProxyURL).FetchOAuthUsage(ctx, accessToken(auth))
}
