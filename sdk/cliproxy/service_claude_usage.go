package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudeusage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// runClaudeUsagePolling keeps Claude credentials' reset times known so
// soonest-reset routing can rank accounts before they carry traffic, and
// their plans known so usage can be compared across plans.
func (s *Service) runClaudeUsagePolling(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	cfg := func() *config.Config { return s.currentConfig() }
	go claudeusage.NewPlanTracker(s.coreManager, cfg).Run(ctx)
	claudeusage.NewPoller(s.coreManager, cfg).Run(ctx)
}
