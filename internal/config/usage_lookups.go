package config

// DefaultUsageLookupSettleSeconds bounds active-account lookup age without polling Anthropic for every response.
const DefaultUsageLookupSettleSeconds = 60

// UsageLookupsConfig controls traffic-driven Claude usage lookups. The legacy YAML key remains compatible.
type UsageLookupsConfig struct {
	SettleSeconds *int `yaml:"settle-seconds,omitempty" json:"settle-seconds,omitempty"`
}

// EffectiveSettleSeconds defaults absent values and disables negative values.
func (c UsageLookupsConfig) EffectiveSettleSeconds() int {
	if c.SettleSeconds == nil {
		return DefaultUsageLookupSettleSeconds
	}
	return max(0, *c.SettleSeconds)
}
