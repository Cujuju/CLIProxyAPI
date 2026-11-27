package config

import "testing"

func TestAntigravityDirectAPIConfigV8(t *testing.T) {
	off, err := ParseConfigBytes([]byte("config-version: 8\noauth:\n  providers:\n    antigravity:\n      tos-pause-window: \"0\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if off.AntigravityDirectAPIEnabled() {
		t.Fatal("direct API must stay off unless the config turns it on")
	}

	on, err := ParseConfigBytes([]byte("config-version: 8\noauth:\n  providers:\n    antigravity:\n      direct-api: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !on.AntigravityDirectAPIEnabled() {
		t.Fatalf("antigravity = %+v, want direct API on", on.Antigravity)
	}
}
