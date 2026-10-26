package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexusage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// runCodexUsagePolling keeps Codex credentials' rate-limit reset credits known for the desktop API.
func (s *Service) runCodexUsagePolling(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	codexusage.NewTracker(s.coreManager, func() *config.Config { return s.currentConfig() }).Run(ctx)
}
