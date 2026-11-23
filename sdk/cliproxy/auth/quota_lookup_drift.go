package auth

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// lookupDriftLineWindows compares Claude and Codex lookups with live headers (Cujuju/spillway#89).
func lookupDriftLineWindows(auth *Auth, lookup []QuotaWindow, lookedUpAt time.Time) string {
	if len(lookup) == 0 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude", "codex":
	default:
		return ""
	}
	header := map[string]float64{}
	for _, w := range signalWindows(auth.Provider, auth.Quota.Signals, auth.Quota.ObservedAt) {
		if w.UsedPercent != nil {
			header[w.Key] = *w.UsedPercent
		}
	}
	lookup = slices.Clone(lookup)
	slices.SortFunc(lookup, func(a, b QuotaWindow) int { return strings.Compare(a.Key, b.Key) })
	pairs := make([]string, 0, len(lookup))
	for _, w := range lookup {
		if w.UsedPercent == nil {
			continue
		}
		headerText := "-"
		if v, ok := header[w.Key]; ok {
			headerText = percentText(v)
		}
		pairs = append(pairs, fmt.Sprintf("%s=%s/%s", w.Key, percentText(*w.UsedPercent), headerText))
	}
	headerAge := "none"
	if !auth.Quota.ObservedAt.IsZero() {
		headerAge = lookedUpAt.Sub(auth.Quota.ObservedAt).Round(time.Second).String()
	}
	return fmt.Sprintf("quota lookup vs response headers (lookup/header %%): auth_index=%s provider=%s header_age=%s %s",
		auth.EnsureIndex(), auth.Provider, headerAge, strings.Join(pairs, " "))
}

func percentText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
