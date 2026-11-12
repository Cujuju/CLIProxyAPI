package auth

import (
	"context"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const codexQuotaUsedPercent = 100

func hasRejectedCodexSignals(signals map[string]string) bool {
	for key, value := range signals {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "x-codex-") {
			continue
		}
		if strings.HasSuffix(lower, "-limit-reached") && strings.EqualFold(value, "true") {
			return true
		}
		if strings.HasSuffix(lower, "-allowed") && strings.EqualFold(value, "false") {
			return true
		}
		if strings.HasSuffix(lower, "-used-percent") {
			used, errParse := strconv.ParseFloat(value, 64)
			if errParse == nil && !math.IsInf(used, 0) && used >= codexQuotaUsedPercent {
				return true
			}
		}
	}
	return false
}

func codexSignalsFromEvaluatedQuota(q QuotaState) (bool, map[string]string) {
	if hasRejectedCodexSignals(q.cooldownSignals) {
		return true, maps.Clone(q.cooldownSignals)
	}
	if q.cooldownEvaluated {
		return false, nil
	}
	if hasRejectedCodexSignals(q.Signals) {
		return true, maps.Clone(q.Signals)
	}
	return false, nil
}

func codexQuotaCooldownForAuth(auth *Auth, model string, now time.Time) (bool, map[string]string) {
	return quotaCooldownForAuth(auth, model, now, "codex", codexSignalsFromEvaluatedQuota)
}

func codexCooldownMetadataForAuths(ctx context.Context, auths []*Auth, modelForAuth func(*Auth) string, now time.Time, clientHeaders ...http.Header) map[string]string {
	_, signals := cooldownMetadataForAuths(ctx, auths, modelForAuth, now, codexQuotaCooldownForAuth, clientHeaders...)
	return signals
}

func (e *modelCooldownError) appendCodexHeaders(headers http.Header) {
	if !hasRejectedCodexSignals(e.codexSignals) {
		return
	}
	var latestWindow string
	var latestReset int64
	for key, value := range e.codexSignals {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "x-codex-") {
			continue
		}
		headers.Set(key, value)
		window := ""
		for _, suffix := range []string{"Used-Percent", "Reset-At", "Reset-After-Seconds"} {
			if strings.HasSuffix(key, suffix) {
				window = strings.TrimSuffix(key, suffix)
				break
			}
		}
		if window == "" {
			continue
		}
		if value, ok := e.codexSignals[window+"Used-Percent"]; ok {
			used, errUsed := strconv.ParseFloat(value, 64)
			if errUsed != nil || math.IsInf(used, 0) || used < codexQuotaUsedPercent || math.IsNaN(used) {
				continue
			}
		}
		reset, errReset := strconv.ParseInt(e.codexSignals[window+"Reset-At"], 10, 64)
		if errReset != nil {
			seconds, _ := strconv.ParseInt(e.codexSignals[window+"Reset-After-Seconds"], 10, 64)
			reset = time.Now().Unix() + seconds
		}
		if latestWindow == "" || reset > latestReset || (reset == latestReset && window < latestWindow) {
			latestWindow, latestReset = window, reset
		}
	}
	if latestWindow != "" {
		seconds := e.resetSeconds()
		headers.Set(latestWindow+"Reset-At", strconv.FormatInt(time.Now().Unix()+seconds, 10))
		headers.Set(latestWindow+"Reset-After-Seconds", strconv.FormatInt(seconds, 10))
	}
}
