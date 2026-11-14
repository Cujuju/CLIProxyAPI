package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type cooldownCodexTestExecutor struct {
	preFTKeepAliveChatExecutor
}

func (*cooldownCodexTestExecutor) Identifier() string { return "codex" }

func TestCodexCooldownResponsesRoute_PassthroughRateLimitHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	executor := &cooldownCodexTestExecutor{preFTKeepAliveChatExecutor: preFTKeepAliveChatExecutor{chunks: chunks}}
	manager := coreauth.NewManager(nil, coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{}), nil)
	manager.RegisterExecutor(executor)
	const model = "codex-cooldown-route-model"
	const authID = "codex-cooldown-route-auth"
	next := time.Now().Add(7823 * time.Second)
	auth := &coreauth.Auth{
		ID:             authID,
		Provider:       executor.Identifier(),
		Status:         coreauth.StatusActive,
		Unavailable:    true,
		NextRetryAfter: next,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: next,
			Signals: map[string]string{
				"X-Codex-Plan-Type":            "plus",
				"X-Codex-Primary-Used-Percent": "100",
				"X-Codex-Primary-Reset-At":     strconv.FormatInt(next.Add(-10*time.Second).Unix(), 10),
			},
		},
		LastError: &coreauth.Error{Code: "rate_limit_error", Message: "upstream quota exceeded", HTTPStatus: http.StatusTooManyRequests},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	for _, passthrough := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			name := "passthrough_" + strconv.FormatBool(passthrough) + "_stream_" + strconv.FormatBool(streaming)
			t.Run(name, func(t *testing.T) {
				handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: passthrough}, manager))
				router := gin.New()
				router.POST("/v1/responses", handler.Responses)

				requestBody := `{"model":"` + model + `","input":"hi","stream":` + strconv.FormatBool(streaming) + `}`
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()

				before := time.Now().Unix()
				router.ServeHTTP(recorder, request)
				after := time.Now().Unix()

				if recorder.Code != http.StatusTooManyRequests {
					t.Fatalf("status = %d, want 429; body=%s", recorder.Code, recorder.Body.String())
				}
				seconds, errSeconds := strconv.ParseInt(recorder.Header().Get("Retry-After"), 10, 64)
				if errSeconds != nil || seconds <= 0 {
					t.Fatalf("Retry-After = %q, want positive seconds", recorder.Header().Get("Retry-After"))
				}

				if !passthrough {
					for key := range recorder.Header() {
						if strings.HasPrefix(strings.ToLower(key), "x-codex-") {
							t.Fatalf("passthrough=false leaked %s", key)
						}
					}
					return
				}
				if got := recorder.Header().Get("X-Codex-Plan-Type"); got != "plus" {
					t.Fatalf("plan = %q", got)
				}
				if got := recorder.Header().Get("X-Codex-Primary-Used-Percent"); got != "100" {
					t.Fatalf("used percent = %q", got)
				}
				reset, errReset := strconv.ParseInt(recorder.Header().Get("X-Codex-Primary-Reset-At"), 10, 64)
				if errReset != nil || reset < before+seconds || reset > after+seconds {
					t.Fatalf("reset = %d; Retry-After = %d", reset, seconds)
				}
				if got := recorder.Header().Get("X-Codex-Primary-Reset-After-Seconds"); got != strconv.FormatInt(seconds, 10) {
					t.Fatalf("reset after = %q", got)
				}
			})
		}
	}
}
