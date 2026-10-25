package auth

import (
	"errors"
	"net/http"
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

// classifyQuotaExhaustion marks a model cooldown as a final usage limit when every eligible
// credential carries explicit quota evidence. Mixed pools keep the retryable cooldown.
func (m *Manager) classifyQuotaExhaustion(err error, providers []string, model string) error {
	var cooldown *modelCooldownError
	if err == nil || model == "" || !errors.As(err, &cooldown) || cooldown == nil {
		return err
	}
	resetsAt, ok := poolQuotaResetsAt(m.snapshotAuths(), providers, model, time.Now())
	if !ok {
		return err
	}
	cooldown.usageLimitResetsAt = resetsAt
	return err
}

// poolQuotaResetsAt returns when the first eligible credential gets allowance back, and false
// unless every eligible credential is exhausted by explicit evidence.
func poolQuotaResetsAt(auths []*Auth, providers []string, model string, now time.Time) (time.Time, bool) {
	wanted := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		wanted[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	reg := registry.GetGlobalRegistry()
	var earliest time.Time
	eligible := 0
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if _, ok := wanted[canonicalSchedulingProvider(auth.Provider)]; !ok || !reg.ClientSupportsModel(auth.ID, model) {
			continue
		}
		eligible++
		resetsAt, exhausted := credentialQuotaResetsAt(auth, model, now)
		if !exhausted {
			return time.Time{}, false
		}
		if earliest.IsZero() || resetsAt.Before(earliest) {
			earliest = resetsAt
		}
	}
	return earliest, eligible > 0
}

// credentialQuotaResetsAt reports explicit exhaustion: a spent account-wide usage window, or a
// quota cooldown whose last upstream error carries an explicit quota code.
func credentialQuotaResetsAt(auth *Auth, model string, now time.Time) (time.Time, bool) {
	var latest time.Time
	for _, window := range QuotaWindows(auth, now) {
		if window.Scoped || !window.ResetsAt.After(now) {
			continue
		}
		spent := window.Status == quotaWindowRejectedStatus || (window.UsedPercent != nil && *window.UsedPercent >= fullWindowPercent)
		if spent && window.ResetsAt.After(latest) {
			latest = window.ResetsAt
		}
	}
	if !latest.IsZero() {
		return latest, true
	}
	if state := modelStateFor(auth, model); state != nil && hasExplicitQuotaError(state.Quota, state.LastError, now) {
		return state.Quota.NextRecoverAt, true
	}
	if hasExplicitQuotaError(auth.Quota, auth.LastError, now) {
		return auth.Quota.NextRecoverAt, true
	}
	return time.Time{}, false
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
