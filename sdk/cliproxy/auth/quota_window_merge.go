package auth

import "time"

// Providers report whole seconds; Claude lookup fractional RFC 3339 is truncated to Unix seconds.
// Readings can differ by one second; distinct periods are a window apart.
const quotaResetPrecision = time.Second

// mergeQuotaWindows keeps source-only windows and lets a newer lookup win whole.
// Otherwise response fields win (including ties), but usage cannot fall below
// lookup usage when both percentages are known and resets identify the same period.
func mergeQuotaWindows(response []QuotaWindow, observedAt time.Time, lookup []QuotaWindow, lookedUpAt time.Time) []QuotaWindow {
	lookupNewer := lookedUpAt.After(observedAt)
	older, newer := lookup, response
	if lookupNewer {
		older, newer = response, lookup
	}
	byKey := make(map[string]QuotaWindow, len(older)+len(newer))
	for _, window := range older {
		byKey[window.Key] = window
	}
	for _, window := range newer {
		if previous, ok := byKey[window.Key]; ok && !lookupNewer &&
			window.UsedPercent != nil && previous.UsedPercent != nil &&
			sameQuotaPeriod(window, previous) && *previous.UsedPercent > *window.UsedPercent {
			percent := *previous.UsedPercent
			window.UsedPercent = &percent
		}
		byKey[window.Key] = window
	}
	out := make([]QuotaWindow, 0, len(byKey))
	for _, window := range byKey {
		out = append(out, window)
	}
	return out
}

func sameQuotaPeriod(a, b QuotaWindow) bool {
	if a.ResetsAt.IsZero() || b.ResetsAt.IsZero() {
		return false
	}
	delta := a.ResetsAt.Sub(b.ResetsAt)
	return delta >= -quotaResetPrecision && delta <= quotaResetPrecision
}
