package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
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
