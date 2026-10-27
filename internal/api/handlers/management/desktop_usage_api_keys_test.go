package management

import (
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestUsageWindowsIncludesConfigAPIKeysWithoutSecrets(t *testing.T) {
	h := newDesktopUsageHandler(t, "sr")
	secret := "sk-ant-test-secret-value-1234"
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, auth := range []*coreauth.Auth{
		{ID: "key-ready", Provider: "claude", Label: "claude-apikey", Success: 7, Failed: 2,
			Attributes: map[string]string{"api_key": secret, "priority": "5"}},
		{ID: "key-disabled", Provider: "claude", Disabled: true, Status: coreauth.StatusDisabled,
			Attributes: map[string]string{"api_key": "tiny"}},
		{ID: "key-limited", Provider: "claude", Status: coreauth.StatusError,
			StatusMessage: "rate limited for " + secret, Unavailable: true, NextRetryAfter: until,
			Attributes: map[string]string{"api_key": secret},
			Quota:      coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: until}},
	} {
		registerAuthForLookupTest(t, h.authManager, auth)
	}
	rec := serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1")
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "tiny") {
		t.Fatal("usage response exposed an API key")
	}
	payload := decodeDesktopUsage(t, rec)
	keys := 0
	for _, credential := range payload.Credentials {
		if credential.APIKey == nil {
			continue
		}
		keys++
		if len(credential.Windows) != 0 || credential.Plan != nil {
			t.Fatalf("API key has invented subscription quota: %+v", credential)
		}
		switch credential.ID {
		case "key-ready":
			if credential.APIKey.Masked != "sk-a…1234" || credential.APIKey.Success != 7 || credential.APIKey.Failed != 2 {
				t.Fatalf("API key display/counts = %+v", credential.APIKey)
			}
		case "key-disabled":
			if !credential.Disabled || credential.APIKey.Masked != "••••" {
				t.Fatalf("disabled short key = %+v", credential)
			}
		case "key-limited":
			if credential.CooldownUntil == nil || !credential.CooldownUntil.Equal(until) {
				t.Fatalf("API key cooldown = %v, want %v", credential.CooldownUntil, until)
			}
		}
	}
	if keys != 3 || len(payload.Credentials) != 6 {
		t.Fatalf("API keys/credentials = %d/%d, want 3/6", keys, len(payload.Credentials))
	}
}
