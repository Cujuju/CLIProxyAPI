package management

import (
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestDesktopEventReportsCacheReadsAndWrites(t *testing.T) {
	record := coreusage.Record{Detail: coreusage.Detail{InputTokens: 2, OutputTokens: 900, CachedTokens: 80000, CacheCreationTokens: 1500}}
	event := desktopEventFromRecord(record)
	if event.Cached != 80000 || event.CacheWrite != 1500 {
		t.Fatalf("cached=%d cacheWrite=%d, want 80000 and 1500", event.Cached, event.CacheWrite)
	}
}
