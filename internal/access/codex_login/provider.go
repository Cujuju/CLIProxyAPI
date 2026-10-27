// Package codexlogin lets Codex reach the proxy with its own ChatGPT login.
//
// Codex's built-in OpenAI provider sends only its login token and takes no custom
// headers, so the proxy accepts an OpenAI-signed access token for an account it
// already holds as a Codex credential.
package codexlogin

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	// ProviderType identifies this access provider in the registry and usage records.
	ProviderType = "codex-login"

	openAIIssuer      = "https://auth.openai.com"
	openAIKeysURL     = openAIIssuer + "/.well-known/jwks.json"
	openAIAPIAudience = "https://api.openai.com/v1"
	signingAlgorithm  = "RS256"
)

// Accounts returns the ID of the proxy's Codex credential for a ChatGPT account and user.
type Accounts func(accountID, userID string) (authID string, ok bool)

var (
	activeMu sync.Mutex
	active   *provider
)

// Configure sets where the provider finds the proxy's Codex credentials. Call before Sync.
func Configure(accounts Accounts) {
	activeMu.Lock()
	defer activeMu.Unlock()
	active = newProvider(accounts, newKeySet(openAIKeysURL, &http.Client{}, time.Now), time.Now)
}

// Sync registers the provider only while client keys are required, so it never closes an open proxy.
func Sync(cfg *sdkconfig.SDKConfig) {
	activeMu.Lock()
	p := active
	activeMu.Unlock()
	if p == nil || !requiresClientKeys(cfg) {
		sdkaccess.UnregisterProvider(ProviderType)
		return
	}
	p.keys.setClient(util.SetProxy(cfg, &http.Client{}))
	sdkaccess.RegisterProvider(ProviderType, p)
}

func requiresClientKeys(cfg *sdkconfig.SDKConfig) bool {
	if cfg == nil {
		return false
	}
	for _, key := range cfg.APIKeys {
		if strings.TrimSpace(key) != "" {
			return true
		}
	}
	return false
}

type provider struct {
	keys     *keySet
	accounts Accounts
	now      func() time.Time
}

func newProvider(accounts Accounts, keys *keySet, now func() time.Time) *provider {
	return &provider{keys: keys, accounts: accounts, now: now}
}

func (p *provider) Identifier() string { return ProviderType }

func (p *provider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || p.accounts == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	login, errVerify := p.verify(ctx, strings.TrimSpace(token))
	if errors.Is(errVerify, errNotOpenAILogin) {
		return nil, sdkaccess.NewNotHandledError()
	}
	if errVerify != nil {
		log.Debugf("codex login rejected: %v", errVerify)
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	authID, held := p.accounts(login.AccountID, login.UserID)
	if !held {
		log.Debug("codex login rejected: account has no Codex credential in the proxy")
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{
		Provider:  ProviderType,
		Principal: ProviderType + ":" + authID,
		Metadata:  map[string]string{"source": "authorization"},
	}, nil
}

var errNotOpenAILogin = errors.New("not an OpenAI login token")

// Login is the ChatGPT identity carried by an OpenAI token.
type Login struct {
	AccountID string `json:"chatgpt_account_id"`
	UserID    string `json:"chatgpt_user_id"`
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type tokenClaims struct {
	Iss   string          `json:"iss"`
	Aud   json.RawMessage `json:"aud"`
	Exp   int64           `json:"exp"`
	Login Login           `json:"https://api.openai.com/auth"`
}

// verify checks the token's issuer, audience, expiry and OpenAI signature.
func (p *provider) verify(ctx context.Context, token string) (Login, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Login{}, errNotOpenAILogin
	}
	var header tokenHeader
	var claims tokenClaims
	if decodeSegment(parts[0], &header) != nil || decodeSegment(parts[1], &claims) != nil {
		return Login{}, errNotOpenAILogin
	}
	if header.Alg != signingAlgorithm || claims.Iss != openAIIssuer {
		return Login{}, errNotOpenAILogin
	}
	if !hasAudience(claims.Aud, openAIAPIAudience) {
		return Login{}, errors.New("token is not for the OpenAI API")
	}
	if p.now().Unix() >= claims.Exp {
		return Login{}, errors.New("token expired")
	}
	if claims.Login.AccountID == "" || claims.Login.UserID == "" {
		return Login{}, errors.New("token has no ChatGPT account")
	}
	signature, errSig := base64.RawURLEncoding.DecodeString(parts[2])
	if errSig != nil {
		return Login{}, errSig
	}
	key, errKey := p.keys.key(ctx, header.Kid)
	if errKey != nil {
		return Login{}, errKey
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if errVerify := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); errVerify != nil {
		return Login{}, errVerify
	}
	return claims.Login, nil
}

func decodeSegment(segment string, out any) error {
	raw, errDecode := base64.RawURLEncoding.DecodeString(segment)
	if errDecode != nil {
		return errDecode
	}
	return json.Unmarshal(raw, out)
}

// hasAudience accepts the JWT "aud" claim as a string or a list.
func hasAudience(raw json.RawMessage, want string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == want
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	for _, aud := range list {
		if aud == want {
			return true
		}
	}
	return false
}

// CredentialLogin reads the ChatGPT identity of a stored Codex credential.
// Its tokens came from the proxy's own OAuth login, so their claims are read unverified.
func CredentialLogin(metadata map[string]any) (Login, bool) {
	accountID, _ := metadata["account_id"].(string)
	for _, field := range []string{"access_token", "id_token"} {
		token, _ := metadata[field].(string)
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		var claims tokenClaims
		if decodeSegment(parts[1], &claims) != nil || claims.Login.UserID == "" {
			continue
		}
		if accountID == "" {
			accountID = claims.Login.AccountID
		}
		return Login{AccountID: accountID, UserID: claims.Login.UserID}, accountID != ""
	}
	return Login{}, false
}
