package auth

import (
	"context"
	"io"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIsUpstreamServerFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"500", customStatusError{code: http.StatusInternalServerError}, true},
		{"502", customStatusError{code: http.StatusBadGateway}, true},
		{"503", customStatusError{code: http.StatusServiceUnavailable}, true},
		{"504 first-byte timeout", newFirstByteTimeoutError(0), true},
		{"529 overloaded", customStatusError{code: 529}, true},
		{"408", customStatusError{code: http.StatusRequestTimeout}, true},
		{"empty stream", &Error{Code: emptyStreamErrorCode}, true},
		{"connection dropped", io.ErrUnexpectedEOF, true},
		{"400 request", customStatusError{code: http.StatusBadRequest}, false},
		{"401 login", customStatusError{code: http.StatusUnauthorized}, false},
		{"403 forbidden", customStatusError{code: http.StatusForbidden}, false},
		{"429 limit", customStatusError{code: http.StatusTooManyRequests}, false},
		{"client canceled", context.Canceled, false},
	} {
		if got := isUpstreamServerFailure(tc.err); got != tc.want {
			t.Errorf("%s: isUpstreamServerFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// serverErrorsManager registers two credentials for one model; the first has priority.
func serverErrorsManager(t *testing.T, cfg *internalconfig.Config, exec ProviderExecutor, firstRules ...internalconfig.RequestScopedErrorRule) *Manager {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	reg := registry.GetGlobalRegistry()
	for _, a := range []*Auth{
		{ID: "server-errors-1", Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "10"}, Metadata: map[string]any{"request_scoped_errors": firstRules}},
		{ID: "server-errors-2", Provider: "claude", Status: StatusActive},
	} {
		reg.RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
		id := a.ID
		t.Cleanup(func() { reg.UnregisterClient(id) })
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatalf("register %s: %v", a.ID, err)
		}
	}
	m.RegisterExecutor(exec)
	return m
}

func returnServerErrorsConfig() *internalconfig.Config {
	cfg := &internalconfig.Config{}
	cfg.Routing.ServerErrors = ServerErrorsReturn
	return cfg
}

func assertNotBenched(t *testing.T, m *Manager) {
	t.Helper()
	a, _ := m.GetByID("server-errors-1")
	if a.Unavailable || !a.NextRetryAfter.IsZero() {
		t.Fatalf("server-errors-1 benched: unavailable=%v nextRetry=%v", a.Unavailable, a.NextRetryAfter)
	}
	for model, state := range a.ModelStates {
		if state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
			t.Fatalf("server-errors-1 model %s benched until %v", model, state.NextRetryAfter)
		}
	}
}

func TestServerErrorsReturnStaysOnCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"529 overloaded", customStatusError{code: 529, msg: `{"type":"error","error":{"type":"overloaded_error"}}`}},
		{"500", customStatusError{code: http.StatusInternalServerError, msg: "api_error"}},
		{"connection dropped", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tried []string
			exec := &mockCustomErrorExecutor{identifier: "claude", executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				tried = append(tried, auth.ID)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, tc.err
			}}
			m := serverErrorsManager(t, returnServerErrorsConfig(), exec)
			if _, err := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{}); err == nil {
				t.Fatal("Execute succeeded, want the server error")
			}
			if len(tried) != 1 || tried[0] != "server-errors-1" {
				t.Fatalf("tried %v, want only server-errors-1", tried)
			}
			assertNotBenched(t, m)
		})
	}
}

func TestServerErrorsDefaultStillFailsOver(t *testing.T) {
	var tried []string
	exec := &mockCustomErrorExecutor{identifier: "claude", executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		tried = append(tried, auth.ID)
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return cliproxyexecutor.Response{}, customStatusError{code: http.StatusServiceUnavailable}
	}}
	m := serverErrorsManager(t, &internalconfig.Config{}, exec)
	_, _ = m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if len(tried) != 2 {
		t.Fatalf("tried %v, want both credentials", tried)
	}
}

func TestServerErrorsConfiguredRuleWins(t *testing.T) {
	var tried []string
	exec := &mockCustomErrorExecutor{identifier: "claude", executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		tried = append(tried, auth.ID)
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return cliproxyexecutor.Response{}, customStatusError{code: http.StatusServiceUnavailable, msg: "maintenance"}
	}}
	rule := internalconfig.RequestScopedErrorRule{Status: http.StatusServiceUnavailable, Match: []string{"maintenance"}, Action: RequestScopedActionContinue}
	m := serverErrorsManager(t, returnServerErrorsConfig(), exec, rule)
	_, _ = m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if len(tried) != 2 {
		t.Fatalf("tried %v, want the configured continue rule to try both", tried)
	}
}

func TestServerErrorsReturnStreamStaysOnCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream func() (*cliproxyexecutor.StreamResult, error)
	}{
		{"503 before streaming", func() (*cliproxyexecutor.StreamResult, error) {
			return nil, customStatusError{code: http.StatusServiceUnavailable}
		}},
		{"empty stream", func() (*cliproxyexecutor.StreamResult, error) {
			chunks := make(chan cliproxyexecutor.StreamChunk)
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tried []string
			exec := &customStreamMockExecutor{identifier: "claude", streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				tried = append(tried, auth.ID)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return tc.stream()
			}}
			m := serverErrorsManager(t, returnServerErrorsConfig(), exec)
			if _, err := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{}); err == nil {
				t.Fatal("ExecuteStream succeeded, want the server error")
			}
			if len(tried) != 1 || tried[0] != "server-errors-1" {
				t.Fatalf("tried %v, want only server-errors-1", tried)
			}
			assertNotBenched(t, m)
		})
	}
}
