package auth

import (
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/regexp"
)

// QuotaWindow is one credential usage limit, normalized across providers.
type QuotaWindow struct {
	// Key identifies the window, e.g. "five_hour", "seven_day", "primary".
	Key string
	// Label is a short human-readable name, e.g. "5-hour" or "Weekly".
	Label string
	// Duration is the window length; zero when unknown.
	Duration time.Duration
	// UsedPercent is 0-100; nil when unknown.
	UsedPercent *float64
	// ResetsAt is when the window resets; zero when unknown.
	ResetsAt time.Time
	// Status is the upstream window status ("allowed", "allowed_warning",
	// "rejected"); empty when unknown.
	Status string
	// Scoped marks model-, feature- or spend-specific windows. Only unscoped
	// windows describe the whole account and drive routing.
	Scoped bool
	// Cadence says when the next period starts after a reset.
	Cadence ResetCadence
}

// ResetCadence says when a window's next period starts after it resets.
type ResetCadence string

const (
	// ResetCadenceUnknown: the provider does not say.
	ResetCadenceUnknown ResetCadence = ""
	// ResetCadenceFixed: the next period starts at the reset, so resets come every Duration.
	ResetCadenceFixed ResetCadence = "fixed"
	// ResetCadenceOnUse: the next period starts with the first request after the reset.
	ResetCadenceOnUse ResetCadence = "on_use"
)

const (
	claudeUnifiedSignalPrefix = "Anthropic-Ratelimit-Unified-"
	claudeFiveHourToken       = "5h"
	claudeWeeklyToken         = "7d"
	claudeWeeklyFableToken    = "7d_oi"
	// claudePercentPerFraction scales decimal utilization before float conversion.
	claudePercentPerFraction = 100
	hoursPerDay              = 24
	daysPerWeek              = 7
	// minutesPerMonthWindow treats any window of at least 28 days as monthly.
	minutesPerMonthWindow = 28 * hoursPerDay * 60
	fiveHourWindow        = 5 * time.Hour
	weeklyWindow          = daysPerWeek * hoursPerDay * time.Hour
	dailyWindow           = hoursPerDay * time.Hour
	// codexUnstartedTolerance absorbs whole-second resets and lookup latency. A window started
	// less than this before an observation also reads as unstarted.
	codexUnstartedTolerance = time.Minute
	// maxQuotaLookupWindows matches maxQuotaSignalHeaders so normalized lookup
	// snapshots stay bounded in memory, clones, and drift logs.
	maxQuotaLookupWindows = maxQuotaSignalHeaders
)

var codexWindowSignal = regexp.MustCompile(`^X-Codex-(?:(.+)-)?(Primary|Secondary)-(Used-Percent|Window-Minutes|Reset-At|Reset-After-Seconds)$`)

// QuotaWindows returns the credential's known usage windows, sorted with
// unscoped windows first and shorter windows before longer ones.
func QuotaWindows(auth *Auth, now time.Time) []QuotaWindow {
	if auth == nil || (len(auth.Quota.Signals) == 0 && len(auth.Quota.LookupSignals) == 0 && len(auth.Quota.LookupWindows) == 0) {
		return nil
	}
	var windows []QuotaWindow
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude", "codex":
		lookup := cloneQuotaWindows(auth.Quota.LookupWindows)
		if len(lookup) == 0 {
			lookup = signalWindows(auth.Provider, auth.Quota.LookupSignals, auth.Quota.LookedUpAt)
		}
		windows = mergeQuotaWindows(
			signalWindows(auth.Provider, auth.Quota.Signals, auth.Quota.ObservedAt), auth.Quota.ObservedAt,
			lookup, auth.Quota.LookedUpAt,
		)
	case "devin":
		windows = devinQuotaWindows(auth.Quota.Signals)
	}
	if len(windows) == 0 && len(auth.Quota.LookupWindows) > 0 {
		windows = cloneQuotaWindows(auth.Quota.LookupWindows)
	}
	sortQuotaWindows(windows)
	return windows
}

func sortQuotaWindows(windows []QuotaWindow) {
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].Scoped != windows[j].Scoped {
			return !windows[i].Scoped
		}
		if windows[i].Duration != windows[j].Duration {
			return windows[i].Duration < windows[j].Duration
		}
		return windows[i].Key < windows[j].Key
	})
}

func isSpentQuotaWindow(window QuotaWindow) bool {
	return window.Status == QuotaWindowStatusRejected ||
		(window.UsedPercent != nil && *window.UsedPercent >= fullWindowPercent)
}

func retainQuotaWindows(windows []QuotaWindow) []QuotaWindow {
	if windows == nil {
		return nil
	}
	if len(windows) == 0 {
		return []QuotaWindow{}
	}
	ordered := slices.Clone(windows)
	if len(ordered) > maxQuotaLookupWindows {
		// Prioritize spent account-wide windows (latest reset, then shortest duration on
		// tie to match credentialQuotaLimit) before truncating.
		sort.SliceStable(ordered, func(i, j int) bool {
			a, b := ordered[i], ordered[j]
			if a.Scoped != b.Scoped {
				return !a.Scoped
			}
			if !a.Scoped {
				aSpent, bSpent := isSpentQuotaWindow(a), isSpentQuotaWindow(b)
				if aSpent != bSpent {
					return aSpent
				}
				if aSpent && !a.ResetsAt.Equal(b.ResetsAt) {
					return a.ResetsAt.After(b.ResetsAt)
				}
			}
			if a.Duration != b.Duration {
				return a.Duration < b.Duration
			}
			return a.Key < b.Key
		})
		// Preserve the longest-duration spent account-wide window for lapsed cooldown fallback.
		longestSpent := -1
		for i, w := range ordered {
			if !w.Scoped && isSpentQuotaWindow(w) &&
				(longestSpent < 0 || w.Duration > ordered[longestSpent].Duration) {
				longestSpent = i
			}
		}
		if longestSpent >= maxQuotaLookupWindows {
			ordered[maxQuotaLookupWindows-1] = ordered[longestSpent]
		}
		ordered = ordered[:maxQuotaLookupWindows]
	}
	sortQuotaWindows(ordered)
	// Copy into an exact-capacity slice so oversized inputs cannot pin a large backing array.
	retained := make([]QuotaWindow, len(ordered))
	for i, w := range ordered {
		if w.UsedPercent != nil {
			percent := *w.UsedPercent
			w.UsedPercent = &percent
		}
		retained[i] = w
	}
	return retained
}

func cloneQuotaWindows(windows []QuotaWindow) []QuotaWindow {
	cloned := slices.Clone(windows)
	for i := range cloned {
		if cloned[i].UsedPercent != nil {
			percent := *cloned[i].UsedPercent
			cloned[i].UsedPercent = &percent
		}
	}
	return cloned
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

func claudeQuotaWindows(signals map[string]string) []QuotaWindow {
	byToken := map[string]*QuotaWindow{}
	order := []string{}
	for key, value := range signals {
		canonical := http.CanonicalHeaderKey(key)
		if !strings.HasPrefix(canonical, claudeUnifiedSignalPrefix) {
			continue
		}
		rest := canonical[len(claudeUnifiedSignalPrefix):]
		cut := strings.LastIndex(rest, "-")
		if cut <= 0 {
			continue
		}
		token, field := strings.ToLower(rest[:cut]), rest[cut+1:]
		window := byToken[token]
		if window == nil {
			window = NewClaudeQuotaWindow(token)
			byToken[token] = window
			order = append(order, token)
		}
		switch field {
		case "Utilization":
			if fraction, ok := new(big.Rat).SetString(strings.TrimSpace(value)); ok {
				// Keep the upstream decimal exact until the final float64 rounding.
				percent, _ := fraction.Mul(fraction, big.NewRat(claudePercentPerFraction, 1)).Float64()
				window.UsedPercent = &percent
			}
		case "Reset":
			if at, ok := parseQuotaTimestamp(value); ok {
				window.ResetsAt = at
			}
		case "Status":
			window.Status = strings.ToLower(strings.TrimSpace(value))
		case "Label":
			// Usage lookups name codename windows; upstream headers never send this field.
			window.Label = strings.TrimSpace(value)
		}
	}
	out := make([]QuotaWindow, 0, len(order))
	for _, token := range order {
		window := byToken[token]
		if window.UsedPercent == nil && window.ResetsAt.IsZero() && window.Status != QuotaWindowStatusRejected {
			continue
		}
		out = append(out, *window)
	}
	return out
}

// NewClaudeQuotaWindow returns the metadata for a Claude unified quota token.
func NewClaudeQuotaWindow(token string) *QuotaWindow {
	switch token {
	// Claude's session window starts with the first message. Weekly windows keep a
	// fixed schedule: an unused scoped weekly window reports the account's weekly reset.
	case claudeFiveHourToken:
		return &QuotaWindow{Key: "five_hour", Label: "5-hour", Duration: fiveHourWindow, Cadence: ResetCadenceOnUse}
	case claudeWeeklyToken:
		return &QuotaWindow{Key: "seven_day", Label: "Weekly", Duration: weeklyWindow, Cadence: ResetCadenceFixed}
	case claudeWeeklyFableToken:
		return &QuotaWindow{Key: "seven_day_fable", Label: "Weekly · Fable", Duration: weeklyWindow, Scoped: true, Cadence: ResetCadenceFixed}
	}
	if scope, ok := strings.CutPrefix(token, claudeWeeklyToken+"_"); ok {
		return &QuotaWindow{Key: "seven_day_" + scope, Label: "Weekly · " + humanizeWindowToken(scope), Duration: weeklyWindow, Scoped: true, Cadence: ResetCadenceFixed}
	}
	return &QuotaWindow{Key: token, Label: humanizeWindowToken(token), Scoped: true}
}

func humanizeWindowToken(token string) string {
	words := strings.FieldsFunc(token, func(r rune) bool { return r == '_' || r == '-' })
	for i, word := range words {
		if word != "" {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func codexQuotaWindows(signals map[string]string, observedAt time.Time) []QuotaWindow {
	type codexWindow struct {
		usedPercent *float64
		resetsAt    time.Time
		minutes     int
	}
	byKey := map[string]*codexWindow{}
	order := []string{}
	for key, value := range signals {
		match := codexWindowSignal.FindStringSubmatch(http.CanonicalHeaderKey(key))
		if match == nil {
			continue
		}
		scope, slot, field := match[1], strings.ToLower(match[2]), match[3]
		windowKey := slot
		if scope != "" {
			windowKey = strings.ToLower(scope) + ":" + slot
		}
		window := byKey[windowKey]
		if window == nil {
			window = &codexWindow{}
			byKey[windowKey] = window
			order = append(order, windowKey)
		}
		value = strings.TrimSpace(value)
		switch field {
		case "Used-Percent":
			if percent, err := strconv.ParseFloat(value, 64); err == nil {
				window.usedPercent = &percent
			}
		case "Window-Minutes":
			if minutes, err := strconv.Atoi(value); err == nil && minutes > 0 {
				window.minutes = minutes
			}
		case "Reset-At":
			if at, ok := parseQuotaTimestamp(value); ok {
				window.resetsAt = at
			}
		case "Reset-After-Seconds":
			if seconds, err := strconv.ParseFloat(value, 64); err == nil && window.resetsAt.IsZero() && !observedAt.IsZero() {
				window.resetsAt = observedAt.Add(time.Duration(seconds * float64(time.Second)))
			}
		}
	}
	limitNames := map[string]string{}
	for key, value := range signals {
		canonical := http.CanonicalHeaderKey(key)
		if strings.HasPrefix(canonical, "X-Codex-") && strings.HasSuffix(canonical, "-Limit-Name") {
			scope := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(canonical, "X-Codex-"), "-Limit-Name"))
			limitNames[scope] = strings.TrimSpace(value)
		}
	}
	out := make([]QuotaWindow, 0, len(order))
	for _, key := range order {
		window := byKey[key]
		if window.usedPercent == nil && window.resetsAt.IsZero() {
			continue
		}
		scope, _, _ := strings.Cut(key, ":")
		out = append(out, NewCodexQuotaWindow(key, limitNames[scope], window.minutes, window.usedPercent, window.resetsAt, observedAt))
	}
	return out
}

// NewCodexQuotaWindow normalizes a Codex slot key (optionally prefixed with scope:).
func NewCodexQuotaWindow(key, limitName string, minutes int, usedPercent *float64, resetsAt, observedAt time.Time) QuotaWindow {
	scope, _, scoped := strings.Cut(key, ":")
	window := QuotaWindow{
		Key: key, Scoped: scoped, Duration: time.Duration(minutes) * time.Minute,
		UsedPercent: usedPercent, ResetsAt: resetsAt,
	}
	if scoped {
		if limitName == "" {
			limitName = scope
		}
		window.Label = limitName + " · " + windowLabelForMinutes(minutes, "limit")
	} else {
		window.Label = windowLabelForMinutes(minutes, humanizeWindowToken(key))
	}
	markCodexSessionWindow(&window, observedAt)
	return window
}

// markCodexSessionWindow marks Codex's account-wide 5-hour window as starting on use. Until a
// request starts it, Codex reports it unused with a reset a full window after each observation;
// that moving reset is cleared so an unstarted window reads like Claude's (no reset).
func markCodexSessionWindow(window *QuotaWindow, observedAt time.Time) {
	if window.Scoped || window.Duration != fiveHourWindow {
		return
	}
	window.Cadence = ResetCadenceOnUse
	unused := window.UsedPercent != nil && *window.UsedPercent == 0
	if unused && !observedAt.IsZero() && !window.ResetsAt.IsZero() && window.ResetsAt.Sub(observedAt) >= window.Duration-codexUnstartedTolerance {
		window.ResetsAt = time.Time{}
	}
}

func windowLabelForMinutes(minutes int, fallback string) string {
	switch {
	case minutes <= 0:
		return fallback
	case minutes >= minutesPerMonthWindow:
		return "Monthly"
	case minutes == daysPerWeek*hoursPerDay*60:
		return "Weekly"
	case minutes == hoursPerDay*60:
		return "Daily"
	case minutes%(hoursPerDay*60) == 0:
		return fmt.Sprintf("%d-day", minutes/(hoursPerDay*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	default:
		return fmt.Sprintf("%d-minute", minutes)
	}
}

func devinQuotaWindows(signals map[string]string) []QuotaWindow {
	var out []QuotaWindow
	for _, spec := range []struct {
		prefix, key, label string
		duration           time.Duration
	}{
		{"daily", "daily", "Daily", dailyWindow},
		{"weekly", "weekly", "Weekly", weeklyWindow},
	} {
		window := QuotaWindow{Key: spec.key, Label: spec.label, Duration: spec.duration}
		known := false
		if remaining, ok := devinRemainingPercent(signals[spec.prefix+"_quota_remaining_percent"]); ok {
			used := float64(100 - remaining)
			window.UsedPercent = &used
			known = true
		}
		if at, ok := parseQuotaTimestamp(signals[spec.prefix+"_quota_reset_at"]); ok {
			window.ResetsAt = at
			known = true
		}
		if known {
			out = append(out, window)
		}
	}
	return out
}

// parseQuotaTimestamp accepts unix seconds (optionally fractional) or RFC 3339.
func parseQuotaTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second))), true
	}
	if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return at, true
	}
	return time.Time{}, false
}

// accountResetTimes returns the future reset times of the credential's longest
// and shortest unscoped windows. A zero time means unknown.
func accountResetTimes(auth *Auth, now time.Time) (longest, shortest time.Time) {
	var longDuration, shortDuration time.Duration
	haveLong, haveShort := false, false
	for _, window := range QuotaWindows(auth, now) {
		if window.Scoped || !window.ResetsAt.After(now) {
			continue
		}
		if !haveLong || window.Duration > longDuration {
			longest, longDuration, haveLong = window.ResetsAt, window.Duration, true
		}
		if !haveShort || window.Duration < shortDuration {
			shortest, shortDuration, haveShort = window.ResetsAt, window.Duration, true
		}
	}
	if haveLong && haveShort && longDuration == shortDuration {
		shortest = time.Time{}
	}
	return longest, shortest
}
