package management

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudeusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexusage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/sjson"
)

const (
	// WindowStartAccessProvider marks window-start requests in usage records and desktop events.
	WindowStartAccessProvider = "window-start"
	// windowStartTimeout bounds one short reply, login renewal included.
	windowStartTimeout = time.Minute
	// windowStartPrompt asks for the shortest reply; Codex ignores output caps.
	windowStartPrompt = "Reply with OK."
)

// Minimal native requests. Claude caps output at one token; the Codex upstream rejects output caps.
var (
	claudeWindowStartBody = []byte(`{"max_tokens":1,"messages":[{"role":"user","content":"` + windowStartPrompt + `"}]}`)
	codexWindowStartBody  = []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"` + windowStartPrompt + `"}]}]}`)
)

type desktopWindowStartRequest struct {
	AuthIndex string `json:"auth_index"`
	Model     string `json:"model"`
}

// StartUsageWindow sends one minimal request through one Claude or Codex login, so a
// usage window that starts on use starts now. Returns the GET /usage/windows payload.
func (h *Handler) StartUsageWindow(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var body desktopWindowStartRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil || strings.TrimSpace(body.AuthIndex) == "" || strings.TrimSpace(body.Model) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index and model are required"})
		return
	}
	auth := h.authByIndex(strings.TrimSpace(body.AuthIndex))
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	var provider string
	var format sdktranslator.Format
	var payload []byte
	switch {
	case claudeusage.Eligible(auth):
		provider, format, payload = "claude", sdktranslator.FormatClaude, claudeWindowStartBody
	case codexusage.Eligible(auth):
		provider, format, payload = "codex", sdktranslator.FormatOpenAIResponse, codexWindowStartBody
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "window start supports enabled Claude and Codex logins only"})
		return
	}
	if onUseWindowRunning(auth, time.Now()) {
		c.JSON(http.StatusConflict, gin.H{"error": "usage window already running"})
		return
	}
	model := strings.TrimSpace(body.Model)
	payload, errSet := sjson.SetBytes(payload, "model", model)
	if errSet != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSet.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), windowStartTimeout)
	defer cancel()
	ctx = coreusage.WithAccessProvider(ctx, WindowStartAccessProvider)
	_, errExec := h.authManager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model, Payload: payload, Format: format}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    format,
		ResponseFormat:  format,
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey:     auth.ID,
			cliproxyexecutor.RequestedModelMetadataKey: model,
		},
	})
	if errExec != nil {
		response := gin.H{"error": errExec.Error()}
		var status interface{ StatusCode() int }
		if errors.As(errExec, &status) && status.StatusCode() > 0 {
			response["status_code"] = status.StatusCode()
		}
		c.JSON(http.StatusBadGateway, response)
		return
	}
	c.JSON(http.StatusOK, gin.H{"usage": h.usageWindowsPayload(time.Now())})
}

// onUseWindowRunning reports an account-wide window that starts on use and has not reset yet.
func onUseWindowRunning(auth *coreauth.Auth, now time.Time) bool {
	for _, window := range coreauth.QuotaWindows(auth, now) {
		if !window.Scoped && window.Cadence == coreauth.ResetCadenceOnUse && window.ResetsAt.After(now) {
			return true
		}
	}
	return false
}
