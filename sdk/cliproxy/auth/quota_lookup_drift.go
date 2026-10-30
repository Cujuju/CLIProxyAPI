package auth

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lookupDriftLine pairs each lookup window with the latest response-header reading, so the log
// shows whether headers trail lookups (Cujuju/spillway#89). Empty when no window can be compared.
func lookupDriftLine(auth *Auth, lookupSignals map[string]string, lookedUpAt time.Time) string {
	lookup := signalWindows(auth.Provider, lookupSignals, lookedUpAt)
	if len(lookup) == 0 {
		return ""
	}
	header := map[string]float64{}
	for _, w := range signalWindows(auth.Provider, auth.Quota.Signals, auth.Quota.ObservedAt) {
		if w.UsedPercent != nil {
			header[w.Key] = *w.UsedPercent
		}
	}
	sort.Slice(lookup, func(i, j int) bool { return lookup[i].Key < lookup[j].Key })
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

func signalWindows(provider string, signals map[string]string, at time.Time) []QuotaWindow {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return claudeQuotaWindows(signals)
	case "codex":
		return codexQuotaWindows(signals, at)
	}
	return nil
}

func percentText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
