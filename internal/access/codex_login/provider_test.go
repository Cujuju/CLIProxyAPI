package codexlogin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const (
	testKid     = "test-key"
	testAccount = "acct-1"
	testUser    = "user-1"
	testAuthID  = "codex-test.json"
)

var testNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type fixture struct {
	key     *rsa.PrivateKey
	fetches atomic.Int32
	up      atomic.Bool
	clock   time.Time
	p       *provider
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{key: key, clock: testNow}
	f.up.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.fetches.Add(1)
		if !f.up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": testKid, "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(server.Close)
	now := func() time.Time { return f.clock }
	accounts := func(accountID, userID string) (string, bool) {
		return testAuthID, accountID == testAccount && userID == testUser
	}
	f.p = newProvider(accounts, newKeySet(server.URL, server.Client(), now), now)
	return f
}

func (f *fixture) token(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signed := seg(map[string]string{"alg": signingAlgorithm, "kid": kid, "typ": "JWT"}) + "." + seg(claims)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func validClaims() map[string]any {
	return map[string]any{
		"iss": openAIIssuer,
		"aud": []string{openAIAPIAudience},
		"exp": testNow.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_account_id": testAccount,
			"chatgpt_user_id":    testUser,
		},
	}
}

func (f *fixture) auth(token string) (*sdkaccess.Result, *sdkaccess.AuthError) {
	r := httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return f.p.Authenticate(context.Background(), r)
}

func wantCode(t *testing.T, err *sdkaccess.AuthError, code sdkaccess.AuthErrorCode) {
	t.Helper()
	if !sdkaccess.IsAuthErrorCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func TestAcceptsSignedLoginForHeldAccount(t *testing.T) {
	f := newFixture(t)
	res, err := f.auth(f.token(t, testKid, validClaims()))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.Provider != ProviderType || res.Principal != ProviderType+":"+testAuthID {
		t.Fatalf("result = %+v", res)
	}
}

func TestRejectsLoginsItCannotTrust(t *testing.T) {
	f := newFixture(t)
	other := validClaims()
	other["https://api.openai.com/auth"] = map[string]string{"chatgpt_account_id": testAccount, "chatgpt_user_id": "someone-else"}
	expired := validClaims()
	expired["exp"] = testNow.Unix()
	wrongAudience := validClaims()
	wrongAudience["aud"] = "https://example.com"
	tampered := f.token(t, testKid, validClaims())
	tampered = tampered[:len(tampered)-4] + "AAAA"

	for name, token := range map[string]string{
		"unheld account": f.token(t, testKid, other),
		"expired":        f.token(t, testKid, expired),
		"wrong audience": f.token(t, testKid, wrongAudience),
		"bad signature":  tampered,
		"unknown key":    f.token(t, "other-key", validClaims()),
	} {
		_, err := f.auth(token)
		if !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeInvalidCredential) {
			t.Errorf("%s: error = %v, want invalid credential", name, err)
		}
	}
}

func TestLeavesNonOpenAITokensToOtherProviders(t *testing.T) {
	f := newFixture(t)
	foreign := validClaims()
	foreign["iss"] = "https://spillway.local"
	for _, token := range []string{"sk-client-key", f.token(t, testKid, foreign)} {
		_, err := f.auth(token)
		wantCode(t, err, sdkaccess.AuthErrorCodeNotHandled)
	}
	if f.fetches.Load() != 0 {
		t.Fatalf("fetched keys %d times for tokens it doesn't handle", f.fetches.Load())
	}
}

func TestUnknownKeyIDsCannotForceRepeatedFetches(t *testing.T) {
	f := newFixture(t)
	for range 5 {
		_, _ = f.auth(f.token(t, "made-up", validClaims()))
	}
	if got := f.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d, want 1 within the refetch gap", got)
	}
	f.clock = f.clock.Add(refetchGap)
	_, _ = f.auth(f.token(t, "made-up", validClaims()))
	if got := f.fetches.Load(); got != 2 {
		t.Fatalf("fetches = %d, want 2 after the refetch gap", got)
	}
}

func TestKeepsCachedKeysWhenRefreshFails(t *testing.T) {
	f := newFixture(t)
	if _, err := f.auth(f.token(t, testKid, validClaims())); err != nil {
		t.Fatalf("first Authenticate: %v", err)
	}
	f.up.Store(false)
	f.clock = f.clock.Add(keyMaxAge)
	claims := validClaims()
	claims["exp"] = f.clock.Add(time.Hour).Unix()
	if _, err := f.auth(f.token(t, testKid, claims)); err != nil {
		t.Fatalf("Authenticate with endpoint down: %v", err)
	}
	if got := f.fetches.Load(); got != 2 {
		t.Fatalf("fetches = %d, want a refresh attempt once the cache aged out", got)
	}
}

func TestSyncRegistersOnlyWhenClientKeysAreRequired(t *testing.T) {
	Configure(func(string, string) (string, bool) { return "", false })
	t.Cleanup(func() { sdkaccess.UnregisterProvider(ProviderType) })
	registered := func() bool {
		for _, p := range sdkaccess.RegisteredProviders() {
			if p.Identifier() == ProviderType {
				return true
			}
		}
		return false
	}
	Sync(&sdkconfig.SDKConfig{APIKeys: []string{"client-key"}})
	if !registered() {
		t.Fatal("not registered with client keys set")
	}
	Sync(&sdkconfig.SDKConfig{APIKeys: []string{" "}})
	if registered() {
		t.Fatal("registered on an open proxy")
	}
}

func TestCredentialLoginReadsStoredTokens(t *testing.T) {
	f := newFixture(t)
	login, ok := CredentialLogin(map[string]any{
		"account_id":   testAccount,
		"access_token": f.token(t, testKid, validClaims()),
	})
	if !ok || login != (Login{AccountID: testAccount, UserID: testUser}) {
		t.Fatalf("CredentialLogin = %+v, %v", login, ok)
	}
	if _, ok = CredentialLogin(map[string]any{"account_id": testAccount}); ok {
		t.Fatal("matched a credential with no tokens")
	}
}
