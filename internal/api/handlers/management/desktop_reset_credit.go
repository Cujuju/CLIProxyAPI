package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudeusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
)

// UseResetCredit spends one of a Codex or Claude account's rate-limit reset credits. On "reset" the
// credential's cooldown is lifted so routing picks it again; its usage and credits are looked up anew.
func (h *Handler) UseResetCredit(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var body desktopUsageRefreshRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil || strings.TrimSpace(body.AuthIndex) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	auth := h.authByIndex(strings.TrimSpace(body.AuthIndex))
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	ctx := c.Request.Context()
	cfg := func() *config.Config { return h.configSnapshot() }
	var (
		code         string
		windowsReset int
		errConsume   error
		refresh      func() error
	)
	switch {
	case codexusage.Eligible(auth):
		var result codexusage.ConsumeResult
		result, errConsume = codexusage.ConsumeResetCredit(ctx, cfg(), auth)
		code, windowsReset = result.Code, result.WindowsReset
		refresh = func() error { return codexusage.NewTracker(h.authManager, cfg).Refresh(ctx, auth) }
	case claudeusage.Eligible(auth):
		poller := claudeusage.NewPoller(h.authManager, cfg)
		code, errConsume = poller.RedeemResetCredit(ctx, auth)
		refresh = func() error { return poller.Refresh(ctx, auth) }
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "reset credits apply to enabled Codex and Claude logins only"})
		return
	}
	if errConsume != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errConsume.Error()})
		return
	}
	if code == resetcredits.Reset {
		if _, _, errReset := h.authManager.ResetQuota(ctx, auth.ID); errReset != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "reset applied, but the cooldown was not lifted: " + errReset.Error()})
			return
		}
	}
	payload := gin.H{"code": code, "windows_reset": windowsReset}
	if errRefresh := refresh(); errRefresh != nil {
		payload["refresh_error"] = errRefresh.Error()
	}
	payload["usage"] = h.usageWindowsPayload(time.Now())
	c.JSON(http.StatusOK, payload)
}
