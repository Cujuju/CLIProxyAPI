// Package codexusage looks up a Codex account's usage windows and reset credits from ChatGPT's usage endpoint.
package codexusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

	secondsPerMinute = 60
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
	RateLimit           *rateLimit `json:"rate_limit"`
	CodeReviewRateLimit *rateLimit `json:"code_review_rate_limit"`
	ResetCredits        *struct {
		Available  int `json:"available_count"`
		Applicable int `json:"applicable_available_count"`
	} `json:"rate_limit_reset_credits"`
}

type rateLimit struct {
	Allowed         *bool        `json:"allowed"`
	LimitReached    *bool        `json:"limit_reached"`
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	ResetAt            int64    `json:"reset_at"`
}

// quotaHeaders restates the document's windows as the X-Codex-* headers Codex responses carry,
// so a lookup and live traffic feed the same quota windows.
func (doc usageDocument) quotaHeaders() http.Header {
	headers := http.Header{}
	addRateLimitHeaders(headers, "X-Codex-", doc.RateLimit)
	addRateLimitHeaders(headers, "X-Codex-Code-Review-", doc.CodeReviewRateLimit)
	return headers
}

func addRateLimitHeaders(headers http.Header, prefix string, limit *rateLimit) {
	if limit == nil {
		return
	}
	if limit.Allowed != nil {
		headers.Set(prefix+"Allowed", strconv.FormatBool(*limit.Allowed))
	}
	if limit.LimitReached != nil {
		headers.Set(prefix+"Limit-Reached", strconv.FormatBool(*limit.LimitReached))
	}
	addWindowHeaders(headers, prefix+"Primary-", limit.PrimaryWindow)
	addWindowHeaders(headers, prefix+"Secondary-", limit.SecondaryWindow)
}

// addWindowHeaders skips a window missing its usage, length or reset, as the traffic parser does.
func addWindowHeaders(headers http.Header, prefix string, window *usageWindow) {
	minutes := int64(0)
	if window != nil {
		minutes = window.LimitWindowSeconds / secondsPerMinute
	}
	if window == nil || window.UsedPercent == nil || minutes <= 0 || window.ResetAt <= 0 {
		return
	}
	headers.Set(prefix+"Used-Percent", strconv.FormatFloat(*window.UsedPercent, 'f', -1, 64))
	headers.Set(prefix+"Window-Minutes", strconv.FormatInt(minutes, 10))
	headers.Set(prefix+"Reset-At", strconv.FormatInt(window.ResetAt, 10))
}

// FetchFunc returns the raw usage document for one credential.
type FetchFunc func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) ([]byte, error)

// Tracker keeps Codex credentials' usage windows and reset credits current.
type Tracker struct {
	manager *coreauth.Manager
	config  func() *config.Config
	fetch   FetchFunc
	now     func() time.Time
	spacing time.Duration

	mu      sync.Mutex
	backoff map[string]time.Time
	// lookedUp is each credential's last successful lookup by this process. Usage windows live
	// only in memory, so a lookup saved before a restart does not count as fresh.
	lookedUp map[string]time.Time
}

// NewTracker builds a tracker over manager. cfg supplies the live config.
func NewTracker(manager *coreauth.Manager, cfg func() *config.Config) *Tracker {
	return &Tracker{manager: manager, config: cfg, fetch: fetchUsage, now: time.Now, spacing: requestSpacing, backoff: map[string]time.Time{}, lookedUp: map[string]time.Time{}}
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

// RefreshAll looks up every eligible credential now, ignoring staleness and backoff.
// Errors are returned per credential ID.
func (t *Tracker) RefreshAll(ctx context.Context) map[string]error {
	failures := map[string]error{}
	first := true
	for _, auth := range t.manager.List() {
		if !Eligible(auth) {
			continue
		}
		if !first {
			t.wait(ctx)
		}
		first = false
		if ctx.Err() != nil {
			return failures
		}
		if errRefresh := t.Refresh(ctx, auth); errRefresh != nil {
			failures[auth.ID] = errRefresh
		}
	}
	return failures
}

// Refresh looks up one credential now, records its usage windows and stores its reset credits.
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
	lookedUpAt := t.now()
	t.manager.ObserveQuotaLookup(auth.ID, doc.quotaHeaders(), lookedUpAt)
	checkedAt := lookedUpAt.UTC().Format(time.RFC3339)
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
		t.mu.Lock()
		t.lookedUp[auth.ID] = lookedUpAt
		t.mu.Unlock()
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
	t.mu.Lock()
	lookedUpAt, ok := t.lookedUp[auth.ID]
	t.mu.Unlock()
	return !ok || t.now().Sub(lookedUpAt) > MaxAge
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
