package codexusage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestConsumeResetCreditSendsAFreshRedeemIDAndReadsTheOutcome(t *testing.T) {
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		ids = append(ids, req["redeem_request_id"])
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer token-a" || r.Header.Get(accountIDHeader) != "acct-1" {
			t.Errorf("request = %s %v", r.Method, r.Header)
		}
		_, _ = w.Write([]byte(`{"code":"reset","windows_reset":2}`))
	}))
	defer server.Close()
	previous := consumeURL
	consumeURL = server.URL
	defer func() { consumeURL = previous }()

	auth := &coreauth.Auth{ID: "a", Provider: "codex", Metadata: map[string]any{"access_token": "token-a", "account_id": "acct-1"}}
	for range 2 {
		got, errConsume := ConsumeResetCredit(context.Background(), &config.Config{}, auth)
		if errConsume != nil || got.Code != ConsumeReset || got.WindowsReset != 2 {
			t.Fatalf("result = %+v, %v", got, errConsume)
		}
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] == ids[1] {
		t.Fatalf("redeem ids = %v", ids)
	}
}

func TestConsumeResetCreditReportsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	previous := consumeURL
	consumeURL = server.URL
	defer func() { consumeURL = previous }()

	if _, errConsume := ConsumeResetCredit(context.Background(), &config.Config{}, codexOAuth("a")); errConsume == nil {
		t.Fatal("HTTP 401 was not an error")
	}
}
