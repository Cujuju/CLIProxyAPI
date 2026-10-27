package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCodexAccountCallAllowed(t *testing.T) {
	cases := []struct {
		method, target string
		want           bool
	}{
		{http.MethodGet, "/backend-api/wham/usage", true},
		{http.MethodPost, "/backend-api/wham/rate-limit-reset-credits/consume", true},
		{http.MethodGet, "/backend-api/wham/tasks/list?limit=20", true},
		{http.MethodGet, "/backend-api/codex/models?client_version=0.164.0", true},
		{http.MethodPost, "/backend-api/ps/plugins/plugin_1/install", true},
		{http.MethodGet, "/backend-api/connectors/directory/list_workspace", true},
		{http.MethodGet, "/backend-api/accounts/verified_access", true},
		{http.MethodPost, "/backend-api/files", true},
		{http.MethodPost, "/backend-api/files/file_1/uploaded", true},
		// ChatGPT's own data and account admin.
		{http.MethodGet, "/backend-api/conversations", false},
		{http.MethodGet, "/backend-api/memories", false},
		{http.MethodGet, "/backend-api/me", false},
		{http.MethodGet, "/backend-api/accounts/check/v4-2023-04-27", false},
		{http.MethodGet, "/backend-api/files/file_1/download", false},
		{http.MethodDelete, "/backend-api/accounts/verified_access", false},
		// Apps MCP would hand the pool token to connected apps.
		{http.MethodPost, "/backend-api/ps/mcp", false},
		// A listed name must end at a path segment.
		{http.MethodGet, "/backend-api/whamx/usage", false},
		{http.MethodGet, "/backend-api/", false},
		// Paths chatgpt.com could normalise outside the list.
		{http.MethodGet, "/backend-api/wham/../conversations", false},
		{http.MethodGet, "/backend-api/wham/%2e%2e/conversations", false},
		{http.MethodGet, "/backend-api/wham%2F..%2Fconversations", false},
		{http.MethodGet, "/backend-api//wham/usage", false},
		{http.MethodGet, `/backend-api/wham\..\conversations`, false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "http://proxy"+tc.target, nil)
		if got := codexAccountCallAllowed(req); got != tc.want {
			t.Errorf("%s %s: allowed = %v, want %v", tc.method, tc.target, got, tc.want)
		}
	}
}

func TestCodexBackend_RefusedPathNeverReachesChatGPT(t *testing.T) {
	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer upstream.Close()
	h := newCodexBackendHandler(t, upstream.URL, codexPoolAuth("codex-a", "pool-token", "acct-1"))

	if rec := serveCodexBackend(h, "/backend-api/conversations", http.Header{}); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if reached {
		t.Fatal("a refused path reached chatgpt.com with the pool token")
	}
}

func TestCodexBackend_DropsCookiesBothWays(t *testing.T) {
	var cookie string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "pool"})
	}))
	defer upstream.Close()
	h := newCodexBackendHandler(t, upstream.URL, codexPoolAuth("codex-a", "pool-token", "acct-1"))

	engine := gin.New()
	engine.GET("/backend-api/*path", h.CodexBackend)
	req := httptest.NewRequest(http.MethodGet, "/backend-api/wham/usage", nil)
	req.Header.Set("Cookie", "client=1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(closeNotifyRecorder{rec}, req)
	if cookie != "" || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("cookie sent = %q, set-cookie returned = %q, want neither", cookie, rec.Header().Get("Set-Cookie"))
	}
}
