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

func TestDesktopEventReportsSessionID(t *testing.T) {
	event := desktopEventFromRecord(coreusage.Record{SessionID: "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344"})
	if event.SessionID != "claude:ac980658-63bd-4fb3-97ba-8da64cb1e344" {
		t.Fatalf("session_id = %q", event.SessionID)
	}
}

func TestDesktopEventReportsUserAgent(t *testing.T) {
	event := desktopEventFromRecord(coreusage.Record{UserAgent: "codex_cli_rs/0.164.0"})
	if event.UserAgent != "codex_cli_rs/0.164.0" {
		t.Fatalf("user_agent = %q", event.UserAgent)
	}
}
