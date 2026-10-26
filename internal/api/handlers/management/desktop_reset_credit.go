package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// UseResetCredit spends one of a Codex account's rate-limit reset credits. On "reset" the
// credential's cooldown is lifted so routing picks it again; its credit count is looked up anew.
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
	if !codexusage.Eligible(auth) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reset credits apply to enabled Codex logins only"})
		return
	}
	cfg := func() *config.Config { return h.configSnapshot() }
	result, errConsume := codexusage.ConsumeResetCredit(c.Request.Context(), cfg(), auth)
	if errConsume != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errConsume.Error()})
		return
	}
	if result.Code == codexusage.ConsumeReset {
		if _, _, errReset := h.authManager.ResetQuota(c.Request.Context(), auth.ID); errReset != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "reset applied, but the cooldown was not lifted: " + errReset.Error()})
			return
		}
	}
	payload := gin.H{"code": result.Code, "windows_reset": result.WindowsReset}
	if errRefresh := codexusage.NewTracker(h.authManager, cfg).Refresh(c.Request.Context(), auth); errRefresh != nil {
		payload["refresh_error"] = errRefresh.Error()
	}
	payload["usage"] = h.usageWindowsPayload(time.Now())
	c.JSON(http.StatusOK, payload)
}
