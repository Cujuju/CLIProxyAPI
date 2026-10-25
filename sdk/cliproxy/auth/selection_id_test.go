package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type selectionRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *selectionRecorder) record(ctx context.Context) {
	r.mu.Lock()
	r.ids = append(r.ids, usage.SelectionIDFromContext(ctx))
	r.mu.Unlock()
}

func (r *selectionRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// Upstream #6988 scenario: one credential, cooling disabled, 429 then 200 across an outer retry.
func TestSelectionID_NewPerSelectionEvenForSameCredential(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 0, 1)
	registerOverloadAuths(t, m, 1)
	rec := &selectionRecorder{}
	calls := 0
	m.RegisterExecutor(&issue6865Executor{id: "codex", executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		rec.record(ctx)
		calls++
		if calls == 1 {
			return cliproxyexecutor.Response{}, customStatusError{code: http.StatusTooManyRequests, msg: `{"error":{"message":"slow down"}}`}
		}
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}})
	if _, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	ids := rec.snapshot()
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("selection IDs = %q, want two distinct non-empty IDs", ids)
	}
}

type selectionPlugin struct {
	handled bool
	authID  string
	seen    []string
}

func (p *selectionPlugin) PickAuth(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	id, _ := req.Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey].(string)
	p.seen = append(p.seen, id)
	if !p.handled {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	return pluginapi.SchedulerPickResponse{Handled: true, AuthID: p.authID}, true, nil
}

func runPluginSelection(t *testing.T, plugin *selectionPlugin) (pluginIDs, usageIDs []string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 2)
	plugin.authID = ids[0] // highest priority tier, so it is among the candidates
	m.SetPluginScheduler(plugin)
	rec := &selectionRecorder{}
	m.RegisterExecutor(&issue6865Executor{id: "codex", executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		rec.record(ctx)
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}})
	if _, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return plugin.seen, rec.snapshot()
}

func TestSelectionID_PluginPickMatchesUsage(t *testing.T) {
	seen, used := runPluginSelection(t, &selectionPlugin{handled: true})
	if len(seen) != 1 || len(used) != 1 || seen[0] == "" || seen[0] != used[0] {
		t.Fatalf("plugin saw %q, usage carried %q; want one shared ID", seen, used)
	}
}

func TestSelectionID_UnhandledPluginPickNotAttributed(t *testing.T) {
	seen, used := runPluginSelection(t, &selectionPlugin{handled: false})
	if len(seen) != 1 || len(used) != 1 || used[0] == "" || seen[0] == used[0] {
		t.Fatalf("plugin saw %q, usage carried %q; a built-in pick must get its own ID", seen, used)
	}
}
