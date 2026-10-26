package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// readEvent returns the next SSE event name and data, skipping comments.
func readEvent(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()
	var name, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "" && name != "":
			return name, data
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// openDesktopEvents connects to the event stream and consumes the "ready" event.
func openDesktopEvents(t *testing.T, manager *coreauth.Manager) *bufio.Reader {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, path, manager)
	h.SetSupervisorPassword("0123456789abcdef0123456789abcdef")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	srv := httptest.NewServer(s.engine)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v8/management/usage/events", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)
	if name, _ := readEvent(t, reader); name != "ready" {
		t.Fatalf("first event = %q, want ready", name)
	}
	return reader
}

func TestDesktopEventsStreamsUsageLookups(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "a", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"}}); err != nil {
		t.Fatal(err)
	}
	reader := openDesktopEvents(t, manager)
	if !manager.ObserveQuotaLookup("a", http.Header{"Anthropic-Ratelimit-Unified-7d_sonnet-Utilization": {"0.5"}}, time.Now()) {
		t.Fatal("lookup not recorded")
	}
	if name, _ := readEvent(t, reader); name != "usage" {
		t.Fatalf("event = %q, want usage", name)
	}
}

func TestDesktopEventsStreamsUsageRecords(t *testing.T) {
	reader := openDesktopEvents(t, nil)

	coreusage.PublishRecord(context.Background(), coreusage.Record{
		RequestID: "req-1", SelectionID: "sel-1", Provider: "claude", Model: "claude-x", AuthIndex: "3",
		Failed: true, Fail: coreusage.Failure{StatusCode: http.StatusTooManyRequests, Body: `{"error":{"type":"rate_limit_error","message":"slow down"}}`},
		Latency: 1500 * time.Millisecond, RequestedAt: time.Now(),
	})
	name, data := readEvent(t, reader)
	if name != "request" {
		t.Fatalf("event = %q, want request", name)
	}
	var got map[string]any
	if errJSON := json.Unmarshal([]byte(data), &got); errJSON != nil {
		t.Fatalf("data: %v", errJSON)
	}
	if got["selection_id"] != "sel-1" || got["auth_index"] != "3" || got["status_code"] != float64(429) || got["latency_ms"] != float64(1500) || got["error"] == "" {
		t.Fatalf("event = %v", got)
	}
}
