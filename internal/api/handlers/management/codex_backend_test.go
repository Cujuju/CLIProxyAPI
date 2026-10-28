package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// newCodexBackendHandler points the handler at upstream and pools the given credentials.
func newCodexBackendHandler(t *testing.T, upstream string, auths ...*coreauth.Auth) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	previous := chatGPTBackendOrigin
	chatGPTBackendOrigin = upstream
	t.Cleanup(func() { chatGPTBackendOrigin = previous })
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	for _, auth := range auths {
		registerAuthForLookupTest(t, manager, auth)
	}
	return NewHandlerWithoutConfigFilePath(cfg, manager)
}

// closeNotifyRecorder adds the CloseNotifier that gin's writer asserts for a reverse proxy; real servers have it.
type closeNotifyRecorder struct{ *httptest.ResponseRecorder }

func (closeNotifyRecorder) CloseNotify() <-chan bool { return make(chan bool) }

func serveCodexBackend(h *Handler, target string, header http.Header) *httptest.ResponseRecorder {
	engine := gin.New()
	engine.GET("/backend-api/*path", h.CodexModels)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header = header
	rec := httptest.NewRecorder()
	engine.ServeHTTP(closeNotifyRecorder{rec}, req)
	return rec
}

func codexPoolAuth(id, token, accountID string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"access_token": token, "account_id": accountID}}
}

func TestCodexBackend_SendsTheCallAsThePoolAccount(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"plan_type":"plus"}`))
	}))
	defer upstream.Close()
	h := newCodexBackendHandler(t, upstream.URL, codexPoolAuth("codex-a", "pool-token", "acct-1"))

	rec := serveCodexBackend(h, "/backend-api/codex/models?key=client-key&x=1", http.Header{
		"Authorization":      {"Bearer client-key"},
		"X-Api-Key":          {"client-key"},
		"Chatgpt-Account-Id": {"client-account"},
	})
	if rec.Code != http.StatusOK || rec.Body.String() != `{"plan_type":"plus"}` {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if got.URL.Path != "/backend-api/codex/models" || got.URL.RawQuery != "x=1" {
		t.Fatalf("upstream URL = %s, want the same path without the client key", got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer pool-token" || got.Header.Get("X-Api-Key") != "" {
		t.Fatalf("upstream auth = %q, x-api-key = %q: the client key must not reach chatgpt.com", got.Header.Get("Authorization"), got.Header.Get("X-Api-Key"))
	}
	if got.Header.Get("Chatgpt-Account-Id") != "acct-1" {
		t.Fatalf("account header = %q, want the pool account's", got.Header.Get("Chatgpt-Account-Id"))
	}
}

func TestCodexBackend_RejectedPoolTokenIsAGatewayError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	h := newCodexBackendHandler(t, upstream.URL, codexPoolAuth("codex-a", "expired", "acct-1"))
	// A 401 would make Codex refresh a login that has no real refresh token.
	if rec := serveCodexBackend(h, "/backend-api/codex/models", http.Header{}); rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestCodexBackend_FailedForwardStatus(t *testing.T) {
	// Port 1 refuses connections: an upstream failure is a gateway error.
	h := newCodexBackendHandler(t, "http://127.0.0.1:1", codexPoolAuth("codex-a", "pool-token", "acct-1"))
	if rec := serveCodexBackend(h, "/backend-api/codex/models", http.Header{}); rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable upstream: status = %d, want 502", rec.Code)
	}

	// A client that hung up is not a gateway failure (Codex cancels calls as it exits).
	engine := gin.New()
	engine.GET("/backend-api/*path", h.CodexModels)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(closeNotifyRecorder{rec}, httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil).WithContext(ctx))
	if rec.Code != clienterror.StatusClientClosedRequest {
		t.Fatalf("client gone: status = %d, want %d", rec.Code, clienterror.StatusClientClosedRequest)
	}
}

func TestCodexBackend_NoCodexAccount(t *testing.T) {
	h := newCodexBackendHandler(t, "http://127.0.0.1:1")
	if rec := serveCodexBackend(h, "/backend-api/codex/models", http.Header{}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
