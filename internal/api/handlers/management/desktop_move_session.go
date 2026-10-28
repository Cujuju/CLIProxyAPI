package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type moveSessionRequest struct {
	// SessionID is a request event's session_id.
	SessionID string `json:"session_id"`
	AuthIndex string `json:"auth_index"`
}

// MoveSession rebinds a conversation, and its subagents, to another credential of the same provider.
func (h *Handler) MoveSession(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var body moveSessionRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil || strings.TrimSpace(body.SessionID) == "" || strings.TrimSpace(body.AuthIndex) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body needs session_id and auth_index"})
		return
	}
	target := h.authByIndex(body.AuthIndex)
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	bound, ok := h.authManager.MoveSession(strings.TrimSpace(body.SessionID), target)
	switch {
	case !ok:
		c.JSON(http.StatusConflict, gin.H{"error": "session affinity is off"})
	case bound == 0:
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation has no live binding for this credential's provider"})
	default:
		c.JSON(http.StatusOK, gin.H{"bound": bound})
	}
}
