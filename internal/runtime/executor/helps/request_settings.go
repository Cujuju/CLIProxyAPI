package helps

import (
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func extractRequestSettings(payload []byte, format string) *usage.RequestSettings {
	settings := &usage.RequestSettings{}
	node := gjson.ParseBytes(payload)
	var maxTokens gjson.Result
	switch format {
	case "claude":
		switch mode := node.Get("thinking.type").String(); mode {
		case "adaptive", "enabled", "disabled":
			settings.Thinking = mode
		}
		if budget := node.Get("thinking.budget_tokens"); budget.Type == gjson.Number {
			value := budget.Int()
			settings.ThinkingBudget = &value
		}
		maxTokens = node.Get("max_tokens")
	case "openai", "openai-response", "codex":
		maxTokens = firstExistingUsageNode(node, "max_completion_tokens", "max_output_tokens", "max_tokens")
		if firstExistingUsageNode(node, "reasoning.effort", "reasoning_effort").String() == "none" {
			settings.Thinking = "disabled"
		}
	}
	if maxTokens.Type == gjson.Number {
		value := maxTokens.Int()
		settings.MaxTokens = &value
	}
	if settings.Thinking == "" && settings.ThinkingBudget == nil && settings.MaxTokens == nil {
		return nil
	}
	return settings
}

// SetRequestBetas snapshots the final upstream Anthropic beta header.
func (r *UsageReporter) SetRequestBetas(headers http.Header) {
	if r == nil {
		return
	}
	unique := map[string]bool{}
	for _, value := range headers.Values("Anthropic-Beta") {
		for _, beta := range strings.Split(value, ",") {
			if beta = strings.TrimSpace(beta); beta != "" {
				unique[beta] = true
			}
		}
	}
	if r.requestSettings == nil && len(unique) == 0 {
		return
	}
	if r.requestSettings == nil {
		r.requestSettings = &usage.RequestSettings{}
	}
	r.requestSettings.Betas = nil
	for beta := range unique {
		r.requestSettings.Betas = append(r.requestSettings.Betas, beta)
	}
	sort.Strings(r.requestSettings.Betas)
}
