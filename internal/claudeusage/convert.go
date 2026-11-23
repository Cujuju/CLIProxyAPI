// Package claudeusage looks up Claude subscription usage out of band so
// routing knows every account's reset times before it sends traffic.
package claudeusage

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	fullWindowPercent = 100
	// fableModelName is the scoped weekly model the upstream 7d_oi window tracks.
	fableModelName = "fable"
)

type usageLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	Severity string   `json:"severity"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			ID          *string `json:"id"`
			DisplayName *string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type usageWindow struct {
	Utilization  *float64 `json:"utilization"`
	ResetsAt     *string  `json:"resets_at"`
	LimitDollars *float64 `json:"limit_dollars"`
}

// WindowsFromUsage converts an /api/oauth/usage document into normalized quota windows.
func WindowsFromUsage(raw []byte) ([]coreauth.QuotaWindow, error) {
	var document map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &document); errUnmarshal != nil {
		return nil, fmt.Errorf("parse Claude usage: %w", errUnmarshal)
	}
	windows := map[string]coreauth.QuotaWindow{}
	covered := map[string]bool{}
	if rawLimits, ok := document["limits"]; ok {
		var limits []usageLimit
		if errLimits := json.Unmarshal(rawLimits, &limits); errLimits == nil {
			for _, limit := range limits {
				token, key := limitToken(limit)
				if token == "" || limit.Percent == nil {
					continue
				}
				setWindow(windows, token, *limit.Percent, limit.ResetsAt, limitStatus(limit.Severity, *limit.Percent), "")
				covered[key] = true
			}
		}
	}
	for key, rawValue := range document {
		if covered[key] || key == "extra_usage" {
			continue
		}
		var window usageWindow
		if errWindow := json.Unmarshal(rawValue, &window); errWindow != nil || window.Utilization == nil {
			continue
		}
		reset := ""
		if window.ResetsAt != nil {
			reset = *window.ResetsAt
		}
		label := ""
		if window.LimitDollars != nil {
			label = fmt.Sprintf("Spend limit $%.0f", *window.LimitDollars)
		}
		setWindow(windows, tokenForKey(key), *window.Utilization, reset, "", label)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("parse Claude usage: no usage windows in response")
	}
	out := slices.Collect(maps.Values(windows))
	slices.SortFunc(out, func(a, b coreauth.QuotaWindow) int {
		if a.Scoped != b.Scoped {
			if !a.Scoped {
				return -1
			}
			return 1
		}
		if a.Duration != b.Duration {
			if a.Duration < b.Duration {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out, nil
}

// limitToken maps a limits[] entry to its header token and the top-level key
// it duplicates.
func limitToken(limit usageLimit) (token, coveredKey string) {
	switch limit.Kind {
	case "session":
		return "5h", "five_hour"
	case "weekly_all":
		return "7d", "seven_day"
	case "weekly_scoped":
		name := ""
		if limit.Scope != nil && limit.Scope.Model != nil {
			if limit.Scope.Model.DisplayName != nil {
				name = *limit.Scope.Model.DisplayName
			} else if limit.Scope.Model.ID != nil {
				name = *limit.Scope.Model.ID
			}
		}
		slug := slugify(name)
		if slug == fableModelName {
			return "7d_oi", ""
		}
		if slug == "" {
			return "", ""
		}
		return "7d_" + slug, "seven_day_" + slug
	}
	return "", ""
}

func tokenForKey(key string) string {
	switch key {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	}
	return slugify(key)
}

func slugify(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == ' ' || r == '-' || r == '.':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// limitStatus maps severity to the unified header status vocabulary.
func limitStatus(severity string, percent float64) string {
	switch {
	case percent >= fullWindowPercent:
		return coreauth.QuotaWindowStatusRejected
	case strings.EqualFold(severity, "normal") || severity == "":
		return coreauth.QuotaWindowStatusAllowed
	default:
		return "allowed_warning"
	}
}

func setWindow(windows map[string]coreauth.QuotaWindow, token string, percent float64, resetsAt, status, label string) {
	if token == "" {
		return
	}
	window, exists := windows[token]
	if !exists {
		window = *coreauth.NewClaudeQuotaWindow(token)
	}
	if percent == 0 {
		percent = 0 // The legacy fraction parser canonicalized negative zero.
	}
	window.UsedPercent = &percent
	if at, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(resetsAt)); errParse == nil {
		// Legacy lookup signals carried whole Unix seconds.
		window.ResetsAt = time.Time{}
		if seconds := at.Unix(); seconds > 0 {
			window.ResetsAt = time.Unix(seconds, 0)
		}
	}
	if status != "" {
		window.Status = status
	}
	if label != "" {
		window.Label = label
	}
	windows[token] = window
}
