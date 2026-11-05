package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func (h *Handler) LeaseCaptures(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected enabled boolean"})
		return
	}
	logging.DesktopCaptures.Lease(*body.Enabled)
	c.JSON(http.StatusOK, gin.H{"leaseSeconds": 60, "bodyLimit": logging.CaptureBodyLimit})
}

func (h *Handler) GetCaptures(c *gin.Context) {
	after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor"})
		return
	}
	c.JSON(http.StatusOK, logging.DesktopCaptures.Page(after))
}
