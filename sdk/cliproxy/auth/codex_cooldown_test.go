package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestModelCooldownError_CodexRateLimitHeaders(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, scope := range []string{"credential", "model", "auth"} {
			name := scope + "_affinity_" + strconv.FormatBool(affinity)
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				var selector Selector
				if affinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
				store := NewFileCooldownStateStore(t.TempDir())
				manager.SetCooldownStateStore(store)
				model := "codex-cooldown-" + name
				for _, id := range []string{"later", "earlier"} {
					authID := model + id
					registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
					if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
						t.Fatal(errRegister)
					}
					delay := 2 * time.Hour
					if id == "later" {
						delay = 4 * time.Hour
					}
					markCtx := internallogging.WithResponseHeadersHolder(ctx)
					internallogging.SetResponseHeaders(markCtx, http.Header{
						"X-Codex-Plan-Type":              {id},
						"X-Codex-Limit-Reached":          {"true"},
						"X-Codex-Primary-Used-Percent":   {"100"},
						"X-Codex-Primary-Reset-At":       {strconv.FormatInt(time.Now().Add(delay).Unix(), 10)},
						"X-Codex-Secondary-Used-Percent": {"25"},
						"X-Codex-Secondary-Reset-At":     {"1900000000"},
					})
					resultModel := model
					if scope == "auth" {
						resultModel = ""
					}
					staleSeq := manager.nextExecutionSeq()
					manager.MarkResult(markCtx, Result{AuthID: authID, Provider: "codex", Model: resultModel, CredentialScope: scope == "credential", RetryAfter: &delay, Error: &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: 429, Message: "usage limit"}})
					// A shorter failure and a late success must not replace the active window.
					lateCtx := internallogging.WithResponseHeadersHolder(ctx)
					internallogging.SetResponseHeaders(lateCtx, http.Header{"X-Codex-Plan-Type": {"stale"}, "X-Codex-Secondary-Used-Percent": {"100"}})
					shorter := time.Minute
					manager.MarkResult(lateCtx, Result{AuthID: authID, Provider: "codex", Model: resultModel, CredentialScope: scope == "credential", RetryAfter: &shorter, Error: &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: 429, Message: "usage limit"}})
					internallogging.SetResponseHeaders(lateCtx, http.Header{"X-Codex-Primary-Used-Percent": {"10"}})
					manager.MarkResult(lateCtx, Result{AuthID: authID, Provider: "codex", Model: resultModel, Success: true, ExecutionSeq: staleSeq})
				}
				check := func(manager *Manager) {
					t.Helper()
					_, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
					var cd *modelCooldownError
					if !errors.As(errExecute, &cd) {
						t.Fatalf("error = %v, want cooldown", errExecute)
					}
					before := time.Now().Unix()
					headers := cd.Headers()
					after := time.Now().Unix()
					if got := headers.Get("X-Codex-Plan-Type"); got != "earlier" {
						t.Fatalf("plan = %q, want earlier", got)
					}
					if got := headers.Get("X-Codex-Secondary-Used-Percent"); got != "25" {
						t.Fatalf("secondary used = %q", got)
					}
					if got := headers.Get("X-Codex-Secondary-Reset-At"); got != "1900000000" {
						t.Fatalf("secondary reset = %q", got)
					}
					seconds, errSeconds := strconv.ParseInt(headers.Get("Retry-After"), 10, 64)
					reset, errReset := strconv.ParseInt(headers.Get("X-Codex-Primary-Reset-At"), 10, 64)
					if errSeconds != nil || errReset != nil || reset < before+seconds || reset > after+seconds {
						t.Fatalf("reset %d, retry %d", reset, seconds)
					}
					if got := headers.Get("X-Codex-Primary-Reset-After-Seconds"); got != strconv.FormatInt(seconds, 10) {
						t.Fatalf("relative reset = %q", got)
					}
					if got := SafeResponseHeaders(errExecute).Get("X-Codex-Plan-Type"); got != "" {
						t.Fatalf("safe headers leaked %q", got)
					}
					restored := restoreModelCooldownErrorModel(newModelCooldownErrorForCandidates("", "codex", cd.resetIn, nil, false, nil).withCodexSignals(cd.codexSignals), model)
					if got := restored.(*modelCooldownError).Headers().Get("X-Codex-Plan-Type"); got != "earlier" {
						t.Fatalf("route model restore lost signals: %q", got)
					}
				}
				check(manager)
				manager.PersistCooldownStates(ctx)
				restored := NewManager(nil, selector, nil)
				restored.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
				restored.SetCooldownStateStore(store)
				for _, id := range []string{"later", "earlier"} {
					if _, errRegister := restored.Register(ctx, &Auth{ID: model + id, Provider: "codex", Status: StatusActive}); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
					t.Fatal(errRestore)
				}
				check(restored)
			})
		}
	}
}

func TestModelCooldownError_CodexExclusions(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, kind := range []string{"non_codex", "mixed", "cloudflare", "ordinary", "no_signals"} {
			t.Run(kind+strconv.FormatBool(affinity), func(t *testing.T) {
				var selector Selector
				if affinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})
				model := "codex-exclusion-" + kind + strconv.FormatBool(affinity)
				providers := []string{"codex"}
				if kind == "non_codex" {
					providers = []string{"claude"}
				}
				if kind == "mixed" {
					providers = []string{"codex", "claude"}
				}
				for _, provider := range providers {
					id := model + provider
					next := time.Now().Add(time.Hour)
					signals := map[string]string{"X-Codex-Primary-Used-Percent": "100"}
					reason := "quota"
					if kind == "cloudflare" {
						reason = "cloudflare challenge"
					}
					if kind == "ordinary" {
						signals = map[string]string{"X-Codex-Primary-Used-Percent": "10"}
					}
					if kind == "no_signals" {
						signals = nil
					}
					registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive, Unavailable: true, NextRetryAfter: next, Quota: QuotaState{Exceeded: true, Reason: reason, NextRecoverAt: next, Signals: signals}}); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				_, errExecute := manager.Execute(context.Background(), providers, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				var cd *modelCooldownError
				if !errors.As(errExecute, &cd) {
					t.Fatalf("error = %v, want cooldown", errExecute)
				}
				for key := range cd.Headers() {
					if len(key) >= 8 && key[:8] == "X-Codex-" {
						t.Fatalf("unexpected header %s", key)
					}
				}
			})
		}
	}
}

func TestModelCooldownError_CodexWindowResetAlignment(t *testing.T) {
	for _, namespace := range []string{"", "Additional-Spark-", "Code-Review-"} {
		t.Run(namespace, func(t *testing.T) {
			prefix := "X-Codex-" + namespace
			signals := map[string]string{
				prefix + "Primary-Used-Percent":   "100",
				prefix + "Primary-Reset-At":       "1700000000",
				prefix + "Secondary-Used-Percent": "100",
				prefix + "Secondary-Reset-At":     "1800000000",
				"X-Codex-Plan-Type":               "plus",
			}
			cd := newModelCooldownError("model", "codex", time.Hour).withCodexSignals(signals)
			before := time.Now().Unix()
			headers := cd.Headers()
			after := time.Now().Unix()
			reset, errReset := strconv.ParseInt(headers.Get(prefix+"Secondary-Reset-At"), 10, 64)
			if errReset != nil || reset < before+int64(time.Hour/time.Second) || reset > after+int64(time.Hour/time.Second) {
				t.Fatalf("reset = %d", reset)
			}
			if got := headers.Get(prefix + "Primary-Reset-At"); got != "1700000000" {
				t.Fatalf("earlier window reset = %q", got)
			}
			if got := headers.Get(prefix + "Secondary-Reset-After-Seconds"); got != "3600" {
				t.Fatalf("reset after = %q", got)
			}
			signals["X-Codex-Plan-Type"] = "changed"
			if got := cd.Headers().Get("X-Codex-Plan-Type"); got != "plus" {
				t.Fatalf("signals not cloned: %q", got)
			}
		})
	}
	for _, flag := range []string{"X-Codex-Allowed", "X-Codex-Limit-Reached"} {
		t.Run(flag, func(t *testing.T) {
			value := "true"
			if flag == "X-Codex-Allowed" {
				value = "false"
			}
			cd := newModelCooldownError("model", "codex", time.Hour).withCodexSignals(map[string]string{flag: value, "X-Codex-Primary-Reset-After-Seconds": "10"})
			headers := cd.Headers()
			if got := headers.Get(flag); got != value {
				t.Fatalf("flag = %q", got)
			}
			if got := headers.Get("X-Codex-Primary-Reset-After-Seconds"); got != "3600" {
				t.Fatalf("reset after = %q", got)
			}
		})
	}
}

func TestModelCooldownError_CodexCredentialQuotaWithoutSignals(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		name := "scheduler"
		if affinity {
			name = "session_affinity"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			model := "codex-missing-signals-" + name
			store := NewFileCooldownStateStore(t.TempDir())
			writer := NewManager(nil, nil, nil)
			writer.SetCooldownStateStore(store)
			for _, id := range []string{"earlier", "later"} {
				authID := model + id
				registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				if _, errRegister := writer.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
					t.Fatal(errRegister)
				}
				delay := time.Hour
				markCtx := internallogging.WithResponseHeadersHolder(ctx)
				if id == "earlier" {
					internallogging.SetResponseHeaders(markCtx, http.Header{
						"X-Codex-Plan-Type":            {"plus"},
						"X-Codex-Primary-Used-Percent": {"100"},
					})
				} else {
					delay *= 2
				}
				writer.MarkResult(markCtx, Result{AuthID: authID, Provider: "codex", Model: model, CredentialScope: true, RetryAfter: &delay, Error: &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: http.StatusTooManyRequests, Message: "usage limit"}})
			}
			writer.PersistCooldownStates(ctx)

			var selector Selector
			if affinity {
				selector = NewSessionAffinitySelector(&RoundRobinSelector{})
			}
			restored := NewManager(nil, selector, nil)
			restored.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
			restored.SetCooldownStateStore(store)
			for _, id := range []string{"earlier", "later"} {
				if _, errRegister := restored.Register(ctx, &Auth{ID: model + id, Provider: "codex", Status: StatusActive}); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			_, errExecute := restored.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			var cd *modelCooldownError
			if !errors.As(errExecute, &cd) {
				t.Fatalf("error = %v, want cooldown", errExecute)
			}
			headers := cd.Headers()
			if got := headers.Get("X-Codex-Plan-Type"); got != "plus" {
				t.Fatalf("plan = %q, want plus", got)
			}
			if got := headers.Get("X-Codex-Primary-Used-Percent"); got != "100" {
				t.Fatalf("used percent = %q, want 100", got)
			}
		})
	}
}
