package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const noCodexAccountBody = "no Codex account in the pool"

func serveBackendAPI(s *Server, method, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func TestCodexBackendRouteNeedsAClientKey(t *testing.T) {
	s := newTestServer(t)
	if rec := serveBackendAPI(s, http.MethodGet, "/backend-api/wham/usage", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without key: status = %d, want 401", rec.Code)
	}
	// With a key the call reaches the account forwarder; this fixture pools no Codex account.
	rec := serveBackendAPI(s, http.MethodGet, "/backend-api/wham/usage", "test-key")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), noCodexAccountBody) {
		t.Fatalf("with key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCodexBackendRouteLeavesModelCallsAlone(t *testing.T) {
	s := newTestServer(t)
	rec := serveBackendAPI(s, http.MethodPost, "/backend-api/codex/responses", "test-key")
	if strings.Contains(rec.Body.String(), noCodexAccountBody) {
		t.Fatalf("model call went to the account forwarder: %d %s", rec.Code, rec.Body.String())
	}
}
