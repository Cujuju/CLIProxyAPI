package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Claude Code probes its base URL with HEAD /api/hello; api.anthropic.com answers 200 without a key.
func TestAnthropicHelloAnswersWithoutAKey(t *testing.T) {
	s := newTestServer(t)
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		rec := httptest.NewRecorder()
		s.engine.ServeHTTP(rec, httptest.NewRequest(method, "/api/hello", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s /api/hello: status = %d, want 200", method, rec.Code)
		}
	}
}
