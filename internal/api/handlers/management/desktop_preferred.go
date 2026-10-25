package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type preferredAuthRequest struct {
	Provider string `json:"provider"`
	// AuthIndex selects the credential; empty restores automatic routing.
	AuthIndex string `json:"auth_index"`
}

// GetPreferredAuths returns provider → preferred credential auth_index.
func (h *Handler) GetPreferredAuths(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"preferred": h.preferredAuthIndexes()})
}

// PutPreferredAuth sets or clears the credential a provider uses while it is available.
func (h *Handler) PutPreferredAuth(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var body preferredAuthRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil || strings.TrimSpace(body.Provider) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body needs provider and auth_index (empty for automatic)"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	authID := ""
	if index := strings.TrimSpace(body.AuthIndex); index != "" {
		auth := h.authByIndex(index)
		if auth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), provider) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "credential belongs to another provider"})
			return
		}
		authID = auth.ID
	}
	h.authManager.SetPreferredAuth(provider, authID)
	c.JSON(http.StatusOK, gin.H{"preferred": h.preferredAuthIndexes()})
}

func (h *Handler) preferredAuthIndexes() map[string]string {
	out := map[string]string{}
	for provider, id := range h.authManager.PreferredAuths() {
		if auth, ok := h.authManager.GetByID(id); ok && auth != nil {
			auth.EnsureIndex()
			out[provider] = auth.Index
		}
	}
	return out
}

// preferredNextPick returns the preferred credential when it is usable now; selection uses it first.
func preferredNextPick(preferredID string, group []*coreauth.Auth, now time.Time) string {
	for _, auth := range group {
		if auth == nil || auth.ID != preferredID || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		if _, cooling := coreauth.CredentialCooldownUntil(auth, now); cooling {
			return ""
		}
		return auth.ID
	}
	return ""
}
