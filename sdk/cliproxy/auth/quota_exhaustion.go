package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// Usage-limit responses (upstream router-for-me#6646). When every eligible credential is out of
// quota by explicit upstream evidence, the pool cooldown is final until a reset, so clients must
// not retry or sleep on a multi-hour Retry-After.

// usageLimitErrorType is the OpenAI-style error type clients show as "usage limit reached".
const usageLimitErrorType = "usage_limit_reached"

// HeaderShouldRetry tells the official OpenAI and Anthropic SDKs whether to retry.
const HeaderShouldRetry = "X-Should-Retry"

// explicitQuotaErrorCodes are upstream error codes that only mean an exhausted allowance,
// never a transient rate limit.
var explicitQuotaErrorCodes = map[string]struct{}{
	"usage_limit_reached": {},
	"insufficient_quota":  {},
}

// quotaWindowRejectedStatus is the upstream window status once its allowance is spent.
const quotaWindowRejectedStatus = "rejected"

// fullWindowPercent is a window with nothing left.
const fullWindowPercent = 100

// quotaLimit is when an exhausted pool first gets allowance back.
type quotaLimit struct {
	ResetsAt time.Time
	// Window is the length of the spent window that sets ResetsAt; zero when the evidence is an
	// error code or the window length is unknown.
	Window time.Duration
}

// classifyQuotaExhaustion marks a model cooldown as a final usage limit when every eligible
// credential carries explicit quota evidence. Mixed pools keep the retryable cooldown.
func (m *Manager) classifyQuotaExhaustion(err error, providers []string, model string) error {
	var cooldown *modelCooldownError
	if err == nil || model == "" || !errors.As(err, &cooldown) || cooldown == nil {
		return err
	}
	limit, ok := poolQuotaLimit(m.snapshotAuths(), providers, model, time.Now())
	if !ok {
		return err
	}
	cooldown.usageLimit = limit
	return err
}

// poolQuotaLimit returns when the first eligible credential gets allowance back, and false
// unless every eligible credential is exhausted by explicit evidence.
func poolQuotaLimit(auths []*Auth, providers []string, model string, now time.Time) (quotaLimit, bool) {
	wanted := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		wanted[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	reg := registry.GetGlobalRegistry()
	var earliest quotaLimit
	eligible := 0
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if _, ok := wanted[canonicalSchedulingProvider(auth.Provider)]; !ok || !reg.ClientSupportsModel(auth.ID, model) {
			continue
		}
		eligible++
		limit, exhausted := credentialQuotaLimit(auth, model, now)
		if !exhausted {
			return quotaLimit{}, false
		}
		if earliest.ResetsAt.IsZero() || limit.ResetsAt.Before(earliest.ResetsAt) {
			earliest = limit
		}
	}
	return earliest, eligible > 0
}

// credentialQuotaLimit reports explicit exhaustion: a spent account-wide usage window, or a
// quota cooldown whose last upstream error carries an explicit quota code.
func credentialQuotaLimit(auth *Auth, model string, now time.Time) (quotaLimit, bool) {
	var latest quotaLimit
	for _, window := range QuotaWindows(auth, now) {
		if window.Scoped || !window.ResetsAt.After(now) {
			continue
		}
		spent := window.Status == quotaWindowRejectedStatus || (window.UsedPercent != nil && *window.UsedPercent >= fullWindowPercent)
		if spent && window.ResetsAt.After(latest.ResetsAt) {
			latest = quotaLimit{ResetsAt: window.ResetsAt, Window: window.Duration}
		}
	}
	if !latest.ResetsAt.IsZero() {
		return latest, true
	}
	if state := modelStateFor(auth, model); state != nil && hasExplicitQuotaError(state.Quota, state.LastError, now) {
		return quotaLimit{ResetsAt: state.Quota.NextRecoverAt}, true
	}
	if hasExplicitQuotaError(auth.Quota, auth.LastError, now) {
		return quotaLimit{ResetsAt: auth.Quota.NextRecoverAt}, true
	}
	return quotaLimit{}, false
}

// finalUsageLimitError returns the pool usage limit in place of a request's final 429 when that
// request spent the last credential, so it answers like every later request until a reset.
func (m *Manager) finalUsageLimitError(err error, providers []string, model string) (error, bool) {
	var cooldown *modelCooldownError
	if err == nil || model == "" || statusCodeFromError(err) != http.StatusTooManyRequests || (errors.As(err, &cooldown) && cooldown.IsUsageLimit()) {
		return err, false
	}
	limit, ok := poolQuotaLimit(m.snapshotAuths(), providers, model, time.Now())
	if !ok {
		return err, false
	}
	usageLimit := newModelCooldownErrorWithCause(model, "", time.Until(limit.ResetsAt), err)
	usageLimit.usageLimit = limit
	return usageLimit, true
}

func modelStateFor(auth *Auth, model string) *ModelState {
	key := canonicalModelKey(model)
	for stateModel, state := range auth.ModelStates {
		if state != nil && canonicalModelKey(stateModel) == key {
			return state
		}
	}
	return nil
}

func hasExplicitQuotaError(quota QuotaState, lastErr *Error, now time.Time) bool {
	if !quota.Exceeded || !quota.NextRecoverAt.After(now) || lastErr == nil || lastErr.HTTPStatus != http.StatusTooManyRequests {
		return false
	}
	return isExplicitQuotaErrorBody(lastErr.Message)
}

// isExplicitQuotaErrorBody checks structured error type/code fields only, never free text.
func isExplicitQuotaErrorBody(body string) bool {
	if start := strings.IndexByte(body, '{'); start > 0 {
		body = body[start:]
	}
	if !gjson.Valid(body) {
		return false
	}
	parsed := gjson.Parse(body)
	for _, path := range []string{"error.type", "error.code", "type", "code"} {
		if _, ok := explicitQuotaErrorCodes[strings.ToLower(strings.TrimSpace(parsed.Get(path).String()))]; ok {
			return true
		}
	}
	return false
}

// Claude Code and Codex build their rate-limit state from response headers, never the body, and
// hosts such as T3 Code schedule a resume from that state. A usage-limit 429 therefore repeats
// the pool's reset in each client's own header family.
const (
	codexSignalPrefix      = "X-Codex-"
	claudeFiveHourClaim    = "five_hour"
	claudeWeeklyClaim      = "seven_day"
	claudeSpentUtilization = "1"
)

// usageLimitHeaderPrefixes are the header families setUsageLimitClientHeaders writes.
var usageLimitHeaderPrefixes = []string{claudeUnifiedSignalPrefix, codexSignalPrefix}

// setUsageLimitClientHeaders describes the credential that recovers first as one spent window.
func setUsageLimitClientHeaders(headers http.Header, limit quotaLimit, now time.Time) {
	reset := strconv.FormatInt(limit.ResetsAt.Unix(), 10)
	claim, token := claudeClaimFor(limit, now)
	headers.Set(claudeUnifiedSignalPrefix+"Status", quotaWindowRejectedStatus)
	headers.Set(claudeUnifiedSignalPrefix+"Reset", reset)
	headers.Set(claudeUnifiedSignalPrefix+"Representative-Claim", claim)
	headers.Set(claudeUnifiedSignalPrefix+token+"-Status", quotaWindowRejectedStatus)
	headers.Set(claudeUnifiedSignalPrefix+token+"-Reset", reset)
	headers.Set(claudeUnifiedSignalPrefix+token+"-Utilization", claudeSpentUtilization)

	headers.Set(codexSignalPrefix+"Primary-Used-Percent", strconv.Itoa(fullWindowPercent))
	headers.Set(codexSignalPrefix+"Primary-Reset-At", reset)
	if limit.Window > 0 {
		headers.Set(codexSignalPrefix+"Primary-Window-Minutes", strconv.Itoa(int(limit.Window/time.Minute)))
	}
}

// claudeClaimFor names the limit as Claude does: five-hour or weekly. Without a window length, a
// reset more than five hours away can only be weekly; nearer ones read as five-hour (label only).
func claudeClaimFor(limit quotaLimit, now time.Time) (claim, token string) {
	window := limit.Window
	if window <= 0 {
		window = limit.ResetsAt.Sub(now)
	}
	if window > fiveHourWindow {
		return claudeWeeklyClaim, claudeWeeklyToken
	}
	return claudeFiveHourClaim, claudeFiveHourToken
}

// isUsageLimitHeader reports whether name belongs to a family setUsageLimitClientHeaders writes.
func isUsageLimitHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	for _, prefix := range usageLimitHeaderPrefixes {
		if strings.HasPrefix(canonical, prefix) {
			return true
		}
	}
	return false
}
