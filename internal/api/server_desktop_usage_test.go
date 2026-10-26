package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDesktopRoutesRequireManagementKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()

	routes := map[string]bool{}
	for _, route := range s.engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v8/management/usage/windows"},
		{http.MethodPost, "/v8/management/usage/refresh"},
		{http.MethodPost, "/v8/management/usage/reset-credit"},
		{http.MethodPost, "/v8/management/server/shutdown"},
		{http.MethodGet, "/v8/management/usage/events"},
	} {
		if !routes[route.method+" "+route.path] {
			t.Fatalf("missing route %s %s", route.method, route.path)
		}
		req := httptest.NewRequest(route.method, route.path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		recorder := httptest.NewRecorder()
		s.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without key: status=%d", route.method, route.path, recorder.Code)
		}
	}

	// Each keyless probe is a failed attempt; five lock the address out, so clear them first.
	cleared, _ := gin.CreateTestContext(httptest.NewRecorder())
	h.DeleteLockouts(cleared)

	req := httptest.NewRequest(http.MethodGet, "/v8/management/usage/windows", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-password")
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, req)
	// No auth manager is attached in this fixture.
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("authorized status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
