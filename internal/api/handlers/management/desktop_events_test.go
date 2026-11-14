package management

import (
	"encoding/json"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestDesktopEventBillingFields(t *testing.T) {
	for _, tc := range []struct{ upstream, want string }{
		{"default", "standard"}, {"standard", "standard"}, {"fast", "fast"},
		{"priority", "fast"}, {"flex", "flex"}, {"", ""}, {"auto", ""}, {"unknown", ""},
	} {
		for _, reported := range []bool{false, true} {
			for _, tokens := range []int64{0, 7} {
				var split *int64
				if reported {
					split = coreusage.Int64Ptr(tokens)
				}
				record := coreusage.Record{ServiceTier: "fast", EffectiveServiceTier: "fast", ResponseServiceTier: tc.upstream,
					Detail: coreusage.Detail{CacheCreationTokens: 9, CacheCreation1hTokens: split, CacheCreation12hTokens: split}}
				data, err := json.Marshal(desktopEventFromRecord(record))
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"cache_creation_1h_tokens", "cache_creation_long_tokens"} {
					value, exists := fields[key]
					if exists != reported || (exists && value != float64(tokens)) {
						t.Fatalf("%s: %s", key, data)
					}
				}
				if value, exists := fields["tier"]; exists != (tc.want != "") || (exists && value != tc.want) {
					t.Fatalf("tier: %s, upstream=%q", data, tc.upstream)
				}
				if fields["cache_creation_tokens"] != float64(9) {
					t.Fatalf("total changed: %s", data)
				}
			}
		}
	}
}

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

func TestDesktopEventEffectiveMetadata(t *testing.T) {
	for _, populated := range []bool{false, true} {
		record := coreusage.Record{Model: "gpt-5", Alias: "coding", TraceID: "1234abcd"}
		if populated {
			record.ReasoningEffort = "high"
			record.ParentSessionID = "codex:parent"
			record.DownstreamTransport = "websocket"
			record.UpstreamTransport = "websocket+http"
			record.TransportFallbackReason = "websocket_upgrade_426"
		}
		data, err := json.Marshal(desktopEventFromRecord(record))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err = json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		for key, expected := range map[string]string{
			"reasoning_effort": record.ReasoningEffort, "parent_session_id": record.ParentSessionID,
			"downstream_transport": record.DownstreamTransport, "upstream_transport": record.UpstreamTransport,
			"transport_fallback_reason": record.TransportFallbackReason,
		} {
			value, exists := fields[key]
			if exists != populated || (exists && value != expected) {
				t.Fatalf("%s = %v, present=%v, want %q present=%v", key, value, exists, expected, populated)
			}
		}
		if fields["model"] != "gpt-5" || fields["alias"] != "coding" || fields["trace_id"] != "1234abcd" {
			t.Fatalf("identity semantics changed: %s", data)
		}
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

func TestDesktopEventAttemptObservations(t *testing.T) {
	for _, tc := range []struct{ session, parent, kind, want string }{
		{"codex:root", "", "root", "root"}, {"codex:child", "codex:parent", "root", ""},
		{"", "", "fork", ""}, {"codex:child", "codex:parent", "compaction", "compaction"},
		{"codex:child", "codex:parent", "fork", "fork"}, {"codex:child", "codex:parent", "subagent", "subagent"},
	} {
		record := coreusage.Record{SessionID: tc.session, ParentSessionID: tc.parent, SessionKind: tc.kind, StatusCode: 201, SelectionID: "selection"}
		record.SessionKind = coreusage.NormalizeSessionKind(record.SessionKind, record.SessionID, record.ParentSessionID)
		event := desktopEventFromRecord(record)
		if event.StatusCode != 201 || event.SessionKind != tc.want || event.SelectionID != "selection" {
			t.Fatalf("event = %+v", event)
		}
	}
	event := desktopEventFromRecord(coreusage.Record{Failed: true, StatusCode: 200, Fail: coreusage.Failure{StatusCode: 429}})
	if event.StatusCode != 429 {
		t.Fatalf("failure status = %d", event.StatusCode)
	}
}

func TestDesktopEventReasoningTokensPresence(t *testing.T) {
	for _, tc := range []struct {
		reported bool
		tokens   int64
	}{{false, 0}, {true, 0}, {true, 42}} {
		data, err := json.Marshal(desktopEventFromRecord(coreusage.Record{Detail: coreusage.Detail{ReasoningTokens: tc.tokens, ReasoningTokensReported: tc.reported}}))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		value, exists := fields["reasoning_tokens"]
		if exists != tc.reported {
			t.Fatalf("reported=%v: %s", tc.reported, data)
		}
		if exists {
			var got int64
			if err := json.Unmarshal(value, &got); err != nil || got != tc.tokens {
				t.Fatalf("reasoning_tokens=%s", value)
			}
		}
		if _, exists := fields["request_settings"]; exists {
			t.Fatalf("unknown settings included: %s", data)
		}
	}
}

func TestDesktopEventCanaryPurpose(t *testing.T) {
	event := desktopEventFromRecord(coreusage.Record{Purpose: "canary"})
	if event.Purpose != "canary" {
		t.Fatalf("purpose=%q", event.Purpose)
	}
}

func TestDesktopEventTurnKey(t *testing.T) {
	for _, key := range []string{"", "codex:turn", "claude:1:abcdef012345"} {
		raw, err := json.Marshal(desktopEventFromRecord(coreusage.Record{TurnKey: key}))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		value, exists := fields["turn_key"]
		if !exists {
			t.Fatal("turn_key omitted")
		}
		if key == "" && value != nil || key != "" && value != key {
			t.Fatalf("turn_key = %v", value)
		}
	}
}
