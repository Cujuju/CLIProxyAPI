package config

// DefaultUsageLookupSettleSeconds is the chosen pause marking a finished turn; longer gaps between requests within a turn cost one extra lookup.
const DefaultUsageLookupSettleSeconds = 60

// UsageLookupsConfig controls settled Claude usage lookups.
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
