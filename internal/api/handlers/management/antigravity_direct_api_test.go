package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// antigravityDirectAPI turns on Antigravity's direct Cloud Code API for tests of that mode.
func antigravityDirectAPI(cfg *config.Config) *config.Config {
	cfg.Antigravity.DirectAPI = true
	return cfg
}

func TestRequestAntigravityTokenRefusedInQuotaOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := newAntigravityOAuthService
	newAntigravityOAuthService = func(*config.Config) antigravityOAuthService {
		t.Error("quota-only mode started an Antigravity OAuth session")
		return nil
	}
	t.Cleanup(func() { newAntigravityOAuthService = original })
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	router := gin.New()
	router.GET("/antigravity-auth-url", h.RequestAntigravityToken)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/antigravity-auth-url", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestResolveTokenForAuthRefusesAntigravityInQuotaOnlyMode(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	original := antigravityOAuthTokenURL
	antigravityOAuthTokenURL = server.URL
	t.Cleanup(func() { antigravityOAuthTokenURL = original })
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	auth := &coreauth.Auth{ID: "ag", Provider: "antigravity", Metadata: map[string]any{"type": "antigravity", "refresh_token": "refresh"}}

	_, err := h.resolveTokenForAuth(context.Background(), auth, "")

	if !errors.Is(err, config.ErrAntigravityDirectAPIDisabled) || hits.Load() != 0 {
		t.Fatalf("err = %v, token requests = %d; want quota-only refusal and 0", err, hits.Load())
	}
}

func TestAPICallRefusesAntigravityCredentialInQuotaOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "ag-api-call", Provider: "antigravity", Metadata: map[string]any{"type": "antigravity"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{}, authManager: manager}
	router := gin.New()
	router.POST("/", h.APICall)

	// A literal Authorization header skips $TOKEN$ resolution, so only the auth_index gate stops it.
	body := `{"method":"GET","url":"` + server.URL + `","auth_index":"` + auth.EnsureIndex() + `","header":{"Authorization":"Bearer caller-token"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict || hits.Load() != 0 {
		t.Fatalf("status = %d, upstream requests = %d; want %d and 0: %s", rec.Code, hits.Load(), http.StatusConflict, rec.Body.String())
	}
}

// exchangeCountingAntigravityOAuth counts token exchanges; any other call fails the session.
type exchangeCountingAntigravityOAuth struct{ exchanges atomic.Int32 }

func (*exchangeCountingAntigravityOAuth) BuildAuthURL(string, string) string {
	return "https://accounts.example.invalid/auth"
}

func (s *exchangeCountingAntigravityOAuth) ExchangeCodeForTokens(context.Context, string, string) (*antigravity.TokenResponse, error) {
	s.exchanges.Add(1)
	return nil, errors.New("exchange must not run in quota-only mode")
}

func (*exchangeCountingAntigravityOAuth) FetchUserInfo(context.Context, string) (string, error) {
	return "", errors.New("unexpected user info call")
}

func (*exchangeCountingAntigravityOAuth) FetchProjectID(context.Context, string) (string, error) {
	return "", errors.New("unexpected project call")
}

func TestPendingAntigravityOAuthStopsWhenDirectAPITurnsOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &exchangeCountingAntigravityOAuth{}
	original := newAntigravityOAuthService
	newAntigravityOAuthService = func(*config.Config) antigravityOAuthService { return svc }
	t.Cleanup(func() { newAntigravityOAuthService = original })
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(antigravityDirectAPI(&config.Config{AuthDir: authDir, Port: 8317}), nil)
	router := gin.New()
	router.GET("/antigravity-auth-url", h.RequestAntigravityToken)
	router.GET("/status", h.GetAuthStatus)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/antigravity-auth-url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", rec.Code, rec.Body.String())
	}
	var start struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	t.Cleanup(func() { CancelOAuthSession(start.State) })

	h.SetConfig(&config.Config{AuthDir: authDir, Port: 8317})
	if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", start.State, "ag-code", ""); err != nil {
		t.Fatal(err)
	}
	status := waitForOAuthStatus(t, router, start.State)
	if status.Status != "error" || svc.exchanges.Load() != 0 {
		t.Fatalf("status = %#v, exchanges = %d; want error and 0", status, svc.exchanges.Load())
	}
}
