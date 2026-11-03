package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestObserveQuotaLookupLiftsQuotaCooldown(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			store := &recordingCooldownStateStore{}
			manager.SetCooldownStateStore(store)
			id, model := "lookup-recovery-"+provider, "lookup-model-"+provider
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			independent := &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), LastError: &Error{HTTPStatus: 503}, UpdatedAt: time.Now().Add(-time.Minute)}
			if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive, ModelStates: map[string]*ModelState{"other-model": independent}}); err != nil {
				t.Fatal(err)
			}
			retry := 56 * time.Hour
			manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: model, CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			before, _ := manager.GetByID(id)
			if !before.Quota.Exceeded || len(store.savedRecords()) == 0 {
				t.Fatal("expected persisted quota cooldown")
			}
			lookupAt := before.UpdatedAt.Add(time.Second)
			if !manager.ObserveQuotaLookup(id, recoveryLookupHeaders(provider, false), lookupAt) {
				t.Fatal("lookup not recorded")
			}
			after, _ := manager.GetByID(id)
			if after.Quota.Exceeded || after.Unavailable || !after.NextRetryAfter.IsZero() || after.Status != StatusActive {
				t.Fatalf("cooldown remains: %+v", after)
			}
			state := after.ModelStates[model]
			if state.Quota.Exceeded || state.Unavailable || state.LastError != nil {
				t.Fatalf("model cooldown remains: %+v", state)
			}
			other := after.ModelStates["other-model"]
			if other.LastError == nil || other.LastError.HTTPStatus != 503 || !other.NextRetryAfter.Equal(independent.NextRetryAfter) || !other.Unavailable {
				t.Fatalf("independent model error changed: %+v", other)
			}
			for _, record := range store.savedRecords() {
				if record.Model != "other-model" {
					t.Fatalf("persisted quota cooldown remains: %+v", record)
				}
			}
			if reg.GetModelCount(model) != 1 {
				t.Fatal("registry routing not resumed")
			}
		})
	}
}

func TestObserveQuotaLookupLiftsQuotaAfterModelFailure(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			id := "lookup-after-model-failure-" + provider
			quotaModel, failedModel := "quota-model-"+provider, "failed-model-"+provider
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: quotaModel}, {ID: failedModel}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); err != nil {
				t.Fatal(err)
			}
			retry := 56 * time.Hour
			manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: quotaModel, CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: failedModel, Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "service unavailable"}})
			before, _ := manager.GetByID(id)
			if !before.Quota.Exceeded || before.LastError == nil || before.LastError.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("expected quota cooldown with aggregated model error: %+v", before)
			}
			independent := before.ModelStates[failedModel].credentialQuotaShadow
			if independent == nil || !independent.Unavailable || !independent.NextRetryAfter.After(time.Now()) {
				t.Fatalf("expected independent model cooldown: %+v", independent)
			}
			if !manager.ObserveQuotaLookup(id, recoveryLookupHeaders(provider, false), before.UpdatedAt.Add(time.Second)) {
				t.Fatal("lookup not recorded")
			}
			after, _ := manager.GetByID(id)
			if after.Quota.Exceeded || after.Unavailable || !after.NextRetryAfter.IsZero() {
				t.Fatalf("quota cooldown remains: %+v", after)
			}
			if state := after.ModelStates[quotaModel]; state.Quota.Exceeded || state.Unavailable || state.LastError != nil {
				t.Fatalf("quota model cooldown remains: %+v", state)
			}
			state := after.ModelStates[failedModel]
			if state.Quota.Exceeded || !state.Unavailable || state.Status != StatusError || state.LastError == nil || state.LastError.HTTPStatus != http.StatusServiceUnavailable ||
				state.LastError.Message != independent.LastError.Message || !state.NextRetryAfter.Equal(independent.NextRetryAfter) || !state.UpdatedAt.Equal(independent.UpdatedAt) {
				t.Fatalf("independent model error changed: %+v, want %+v", state, independent)
			}
		})
	}
}

func TestObserveQuotaLookupKeepsCooldownWithoutRecovery(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, scenario := range []string{"older", "same-time", "exhausted", "disabled", "unauthorized", "unauthorized-code"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				now := time.Now()
				quota := QuotaState{Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: now.Add(56 * time.Hour)}
				auth := &Auth{ID: "lookup-keeps", Provider: provider, Status: StatusError, Unavailable: true, NextRetryAfter: quota.NextRecoverAt, Quota: quota, UpdatedAt: now,
					ModelStates: map[string]*ModelState{"model": {Status: StatusError, Unavailable: true, NextRetryAfter: quota.NextRecoverAt, Quota: quota, UpdatedAt: now}}}
				lookupAt := now.Add(time.Second)
				switch scenario {
				case "older":
					lookupAt = now.Add(-time.Second)
				case "same-time":
					lookupAt = now
				case "disabled":
					auth.Disabled = true
				case "unauthorized":
					auth.LastError = &Error{HTTPStatus: 401}
				case "unauthorized-code":
					auth.LastError = &Error{Code: "unauthorized"}
				}
				// Install an exact timestamp so ordering assertions do not depend on a clock tick.
				manager.auths[auth.ID] = auth
				if !manager.ObserveQuotaLookup(auth.ID, recoveryLookupHeaders(provider, scenario == "exhausted"), lookupAt) {
					t.Fatal("lookup not recorded")
				}
				after, _ := manager.GetByID(auth.ID)
				if !after.Quota.Exceeded || !after.Unavailable || !after.NextRetryAfter.Equal(quota.NextRecoverAt) {
					t.Fatalf("cooldown cleared: %+v", after)
				}
			})
		}
	}
}

func recoveryLookupHeaders(provider string, exhausted bool) http.Header {
	if provider == "claude" {
		used := "0.02"
		if exhausted {
			used = "1"
		}
		return http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.01"}, "Anthropic-Ratelimit-Unified-7d-Utilization": {used}}
	}
	used := "2"
	if exhausted {
		used = "100"
	}
	return http.Header{"X-Codex-Primary-Used-Percent": {"1"}, "X-Codex-Secondary-Used-Percent": {used}}
}
