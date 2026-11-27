package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravityLoginRefusedInQuotaOnlyMode(t *testing.T) {
	_, err := AntigravityAuthenticator{}.Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true})
	if !errors.Is(err, config.ErrAntigravityDirectAPIDisabled) {
		t.Fatalf("Login error = %v, want quota-only refusal", err)
	}
}
