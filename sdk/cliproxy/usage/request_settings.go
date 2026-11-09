package usage

// RequestSettings contains observable settings from the final upstream request.
type RequestSettings struct {
	Thinking       string   `json:"thinking,omitempty"`
	ThinkingBudget *int64   `json:"thinking_budget,omitempty"`
	MaxTokens      *int64   `json:"max_tokens,omitempty"`
	Betas          []string `json:"betas,omitempty"`
}
