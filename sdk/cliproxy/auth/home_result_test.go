package auth

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type homeEffortCapture struct {
	ch chan coreusage.Record
}

func (p homeEffortCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if p.ch == nil || record.AuthID != "test-home-effort-unknown" || record.ExecutorType != homeResultExecutorType {
		return
	}
	select {
	case p.ch <- record:
	default:
	}
}

func TestHomeUnauthorizedEffectiveEffortUnknown(t *testing.T) {
	ch := make(chan coreusage.Record, 1)
	name := "test-home-effort-unknown"
	coreusage.RegisterNamedPlugin(name, homeEffortCapture{ch: ch})
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(name, homeEffortCapture{}) })
	ctx := coreusage.WithReasoningEffort(context.Background(), "xhigh")
	m := &Manager{}
	m.reportHomeUnauthorized(ctx, &Auth{ID: name, Index: "test-index"}, "codex", "gpt-5", "synthetic-fingerprint", "")
	select {
	case record := <-ch:
		if record.ReasoningEffort != "" || !record.Failed || record.Fail.StatusCode != 401 {
			t.Fatalf("synthetic failure must not claim requested effort: %+v", record)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no synthetic failure usage record published")
	}
}
