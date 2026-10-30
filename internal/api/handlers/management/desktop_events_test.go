package management

import (
	"encoding/json"
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

func TestDesktopEventUpstreamID(t *testing.T) {
	for _, id := range []string{"msg_1", "resp_1", ""} {
		event := desktopEventFromRecord(coreusage.Record{RequestID: "local", UpstreamID: id})
		if event.UpstreamID != id || event.RequestID != "local" {
			t.Fatalf("event=%+v", event)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var all map[string]json.RawMessage
		if err = json.Unmarshal(data, &all); err != nil {
			t.Fatal(err)
		}
		value, exists := all["upstream_id"]
		if exists != (id != "") {
			t.Fatalf("upstream_id presence=%v, id=%q", exists, id)
		}
		if exists {
			if err = json.Unmarshal(value, &id); err != nil {
				t.Fatal(err)
			}
			if id != event.UpstreamID {
				t.Fatalf("upstream_id=%s", value)
			}
		}
	}
}
