// Package codexusage looks up Codex account details that only ChatGPT's usage endpoint reports.
package codexusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// CheckInterval is how often credentials are checked for a stale lookup.
	CheckInterval = time.Minute
	// MaxAge matches the Claude usage lookups: a spent or granted credit shows within 15 minutes.
	MaxAge = 15 * time.Minute
	// requestSpacing keeps lookups for many accounts from bursting.
	requestSpacing = 2 * time.Second
	// requestTimeout bounds one control-plane lookup.
	requestTimeout = 20 * time.Second
	// maxBodyBytes bounds the usage document read; it is a few kilobytes.
	maxBodyBytes = 1 << 20

	// Auth metadata keys holding the last lookup, so it survives restarts.
	availableKey  = "reset_credits_available"
	applicableKey = "reset_credits_applicable"
	checkedAtKey  = "reset_credits_checked_at"

	accountIDHeader = "Chatgpt-Account-Id"
)

// usageURL is ChatGPT's account usage document, the one Codex's /status reads.
var usageURL = "https://chatgpt.com/backend-api/wham/usage"

// ResetCredits are a Codex account's rate-limit reset credits as of its last lookup.
type ResetCredits struct {
	// Available is how many credits the account holds.
	Available int
	// Applicable is how many can be used now, i.e. against a limit the account has reached.
	Applicable int
	CheckedAt  time.Time
}

// ResetCreditsOf returns the credential's last looked-up reset credits; false when never reported.
func ResetCreditsOf(auth *coreauth.Auth) (ResetCredits, bool) {
	if auth == nil || auth.Metadata == nil {
		return ResetCredits{}, false
	}
	available, okAvailable := metadataInt(auth.Metadata[availableKey])
	applicable, okApplicable := metadataInt(auth.Metadata[applicableKey])
	checkedAt, errParse := time.Parse(time.RFC3339, metadataString(auth, checkedAtKey))
	if !okAvailable || !okApplicable || errParse != nil {
		return ResetCredits{}, false
	}
	return ResetCredits{Available: available, Applicable: applicable, CheckedAt: checkedAt}, true
}

// usageDocument is the part of the usage document the tracker reads.
type usageDocument struct {
	ResetCredits *struct {
		Available  int `json:"available_count"`
		Applicable int `json:"applicable_available_count"`
	} `json:"rate_limit_reset_credits"`
}

// FetchFunc returns the raw usage document for one credential.
type FetchFunc func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) ([]byte, error)

// Tracker keeps Codex credentials' reset credits current.
type Tracker struct {
	manager *coreauth.Manager
	config  func() *config.Config
	fetch   FetchFunc
	now     func() time.Time
	spacing time.Duration

	mu      sync.Mutex
	backoff map[string]time.Time
}

// NewTracker builds a tracker over manager. cfg supplies the live config.
func NewTracker(manager *coreauth.Manager, cfg func() *config.Config) *Tracker {
	return &Tracker{manager: manager, config: cfg, fetch: fetchUsage, now: time.Now, spacing: requestSpacing, backoff: map[string]time.Time{}}
}

// Run refreshes stale credentials immediately and then every CheckInterval until ctx ends.
func (t *Tracker) Run(ctx context.Context) {
	for {
		t.RefreshStale(ctx)
		timer := time.NewTimer(CheckInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// RefreshStale looks up every eligible credential whose lookup is missing or older than MaxAge.
func (t *Tracker) RefreshStale(ctx context.Context) {
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
			log.Debugf("codex reset credit lookup for %s failed: %v", auth.ID, errRefresh)
		}
		t.wait(ctx)
	}
}

// Refresh looks up one credential now and stores its reset credits on the credential.
func (t *Tracker) Refresh(ctx context.Context, auth *coreauth.Auth) error {
	lookupCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	body, errFetch := t.fetch(lookupCtx, t.config(), auth)
	var doc usageDocument
	if errFetch == nil {
		errFetch = json.Unmarshal(body, &doc)
	}
	if errFetch != nil {
		t.setBackoff(auth.ID, t.now().Add(MaxAge))
		return errFetch
	}
	checkedAt := t.now().UTC().Format(time.RFC3339)
	_, _, errModify := t.manager.Modify(ctx, auth.ID, 0, func(current *coreauth.Auth) error {
		if current.Metadata == nil {
			current.Metadata = map[string]any{}
		}
		current.Metadata[checkedAtKey] = checkedAt
		if doc.ResetCredits == nil {
			delete(current.Metadata, availableKey)
			delete(current.Metadata, applicableKey)
			return nil
		}
		current.Metadata[availableKey] = doc.ResetCredits.Available
		current.Metadata[applicableKey] = doc.ResetCredits.Applicable
		return nil
	})
	if errModify == nil {
		t.setBackoff(auth.ID, time.Time{})
	}
	return errModify
}

// Eligible reports an enabled Codex ChatGPT login; API-key credentials have no account usage.
func Eligible(auth *coreauth.Auth) bool {
	if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() == coreauth.AuthKindAPIKey {
		return false
	}
	return metadataString(auth, "access_token") != ""
}

func (t *Tracker) isStale(auth *coreauth.Auth) bool {
	checkedAt, errParse := time.Parse(time.RFC3339, metadataString(auth, checkedAtKey))
	return errParse != nil || t.now().Sub(checkedAt) > MaxAge
}

func (t *Tracker) inBackoff(authID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.backoff[authID]
	return ok && t.now().Before(until)
}

func (t *Tracker) setBackoff(authID string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if until.IsZero() {
		delete(t.backoff, authID)
		return
	}
	t.backoff[authID] = until
}

func (t *Tracker) wait(ctx context.Context) {
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

func fetchUsage(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) ([]byte, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Authorization", "Bearer "+metadataString(auth, "access_token"))
	req.Header.Set("User-Agent", executor.CodexUserAgent)
	if accountID := metadataString(auth, "account_id"); accountID != "" {
		req.Header.Set(accountIDHeader, accountID)
	}
	resp, errDo := helps.NewUtlsHTTPClient(ctx, cfg, auth, requestTimeout).Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage lookup: HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func metadataString(auth *coreauth.Auth, key string) string {
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
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
