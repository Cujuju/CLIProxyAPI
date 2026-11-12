package helps

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestRequestTurnKey(t *testing.T) {
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}
	prompt := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	loop := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":[]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`
	next := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"user","content":"next"}]}`
	first := requestTurnKey(opts, []byte(prompt))

	if first == "" || first != requestTurnKey(opts, []byte(loop)) {
		t.Fatal("tool loop changed turn")
	}
	if first == requestTurnKey(opts, []byte(next)) {
		t.Fatal("new prompt retained turn")
	}
	opts.SourceFormat = sdktranslator.FromString("codex")
	opts.Headers = http.Header{"X-Codex-Turn-Metadata": {`{"turn_id":"one"}`}}
	if got := requestTurnKey(opts, nil); got != "codex:one" {
		t.Fatal(got)
	}
	opts.Headers = nil
	body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"turn_id\":\"two\"}"}}`)
	if got := requestTurnKey(opts, body); got != "codex:two" {
		t.Fatal(got)
	}
	if got := requestTurnKey(opts, []byte(prompt)); got != "" {
		t.Fatal(got)
	}
}

func TestTurnKeyReporter(t *testing.T) {
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex"), Headers: http.Header{"X-Codex-Turn-Metadata": {`{"turn_id":"one"}`}}}
	ctx := EnsureSessionContext(context.Background(), opts, nil)
	reporter := NewUsageReporter(ctx, "codex", "gpt-6.1-sol", nil)
	if got := reporter.buildRecord(usage.Detail{}, false, usage.Failure{}).TurnKey; got != "codex:one" {
		t.Fatal(got)
	}
	reporter.SetTranslatedRequestMetadata([]byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"turn_id\":\"two\"}"}}`), "codex")
	if got := reporter.buildRecord(usage.Detail{}, false, usage.Failure{}).TurnKey; got != "codex:two" {
		t.Fatal(got)
	}
}
