// Package claudeusage looks up Claude subscription usage out of band so
// routing knows every account's reset times before it sends traffic.
package claudeusage

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	signalPrefix = "Anthropic-Ratelimit-Unified-"
	// percentPerFraction converts usage percent to the header's 0-1 fraction.
	percentPerFraction = 100
	// fractionDecimalPlaces is the number of decimal places added by dividing by 100.
	fractionDecimalPlaces = 2
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

// HeadersFromUsage converts an /api/oauth/usage document into the unified
// rate-limit header shape the passive quota observer already records.
func HeadersFromUsage(raw []byte) (http.Header, error) {
	var document map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &document); errUnmarshal != nil {
		return nil, fmt.Errorf("parse Claude usage: %w", errUnmarshal)
	}
	headers := http.Header{}
	covered := map[string]bool{}
	if rawLimits, ok := document["limits"]; ok {
		var limits []usageLimit
		if errLimits := json.Unmarshal(rawLimits, &limits); errLimits == nil {
			for _, limit := range limits {
				token, key := limitToken(limit)
				if token == "" || limit.Percent == nil {
					continue
				}
				setWindow(headers, token, *limit.Percent, limit.ResetsAt, limitStatus(limit.Severity, *limit.Percent), "")
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
		setWindow(headers, tokenForKey(key), *window.Utilization, reset, "", label)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("parse Claude usage: no usage windows in response")
	}
	return headers, nil
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
	case percent >= percentPerFraction:
		return "rejected"
	case strings.EqualFold(severity, "normal") || severity == "":
		return "allowed"
	default:
		return "allowed_warning"
	}
}

func setWindow(headers http.Header, token string, percent float64, resetsAt, status, label string) {
	if token == "" {
		return
	}
	base := signalPrefix + token + "-"
	decimal := strconv.FormatFloat(percent, 'f', -1, 64)
	fraction, _ := new(big.Rat).SetString(decimal)
	// Division by 100 adds two decimal places; retain every original decimal digit.
	decimalPlaces := 0
	if point := strings.IndexByte(decimal, '.'); point >= 0 {
		decimalPlaces = len(decimal) - point - 1
	}
	fraction.Quo(fraction, big.NewRat(percentPerFraction, 1))
	headers.Set(base+"Utilization", strings.TrimRight(strings.TrimRight(fraction.FloatString(decimalPlaces+fractionDecimalPlaces), "0"), "."))
	if at, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(resetsAt)); errParse == nil {
		headers.Set(base+"Reset", strconv.FormatInt(at.Unix(), 10))
	}
	if status != "" {
		headers.Set(base+"Status", status)
	}
	if label != "" {
		headers.Set(base+"Label", label)
	}
}
