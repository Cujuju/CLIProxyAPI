package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type purposeUsagePlugin struct{ records chan usage.Record }

func (p *purposeUsagePlugin) HandleUsage(_ context.Context, record usage.Record) { p.records <- record }

func TestCanaryPurposeCapturedAndStripped(t *testing.T) {
	for _, value := range []string{"thinking-test", "canary", "window-start", "other", ""} {
		t.Run(value, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("X-CPA-Purpose", value)
			handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
			ctx, cancel := handler.GetContextWithCancel(nil, c, context.Background())
			defer cancel()
			if forwarded := modelExecutionHeaders(ctx, nil); forwarded.Get("X-CPA-Purpose") != "" {
				t.Fatal("purpose header retained for forwarding")
			}
			manager := usage.NewManager(1)
			defer manager.Stop()
			plugin := &purposeUsagePlugin{records: make(chan usage.Record, 1)}
			manager.Register(plugin)
			manager.Publish(ctx, usage.Record{})
			select {
			case record := <-plugin.records:
				want := ""
				if value == "canary" || value == "thinking-test" {
					want = value
				}
				if record.Purpose != want {
					t.Fatalf("purpose=%q, want %q", record.Purpose, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("usage record not delivered")
			}
		})
	}
}
