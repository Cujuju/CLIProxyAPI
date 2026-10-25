package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudeusage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// runClaudeUsagePolling keeps Claude credentials' reset times known so
// soonest-reset routing can rank accounts before they carry traffic.
func (s *Service) runClaudeUsagePolling(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	claudeusage.NewPoller(s.coreManager, func() *config.Config { return s.currentConfig() }).Run(ctx)
}
