package config

import (
	"bytes"
	"testing"
)

func TestUsageLookupSettleConfig(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"absent", "", 60},
		{"empty", "usage-lookups: {}\n", 60},
		{"configured", "usage-lookups: {settle-seconds: 30}\n", 30},
		{"off", "usage-lookups: {settle-seconds: 0}\n", 0},
		{"negative", "usage-lookups: {settle-seconds: -1}\n", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("config-version: 8\n" + tt.body)
			if err := ValidateV8Config(data); err != nil {
				t.Fatal(err)
			}
			normalized, _, err := NormalizeConfigLayout(data, true)
			if err != nil {
				t.Fatal(err)
			}
			if tt.body != "" && !bytes.Contains(normalized, []byte("usage-lookups:")) {
				t.Fatalf("key removed: %s", normalized)
			}
			cfg, err := ParseConfigBytes(normalized)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.UsageLookups.EffectiveSettleSeconds(); got != tt.want {
				t.Fatalf("settle = %d, want %d", got, tt.want)
			}
		})
	}
}
