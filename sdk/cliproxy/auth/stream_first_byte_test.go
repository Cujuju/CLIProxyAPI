package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const testFirstByteLimit = 50 * time.Millisecond

func requireFirstByteTimeout(t *testing.T, err error) {
	t.Helper()
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != ErrorCodeFirstByteTimeout || authErr.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("err = %v, want first-byte timeout 504", err)
	}
}

// hangingStart blocks like an upstream that accepted the request and never answers.
func hangingStart(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFirstByteTimeout_HungRequest(t *testing.T) {
	_, err := executeStreamWithFirstByteTimeout(context.Background(), testFirstByteLimit, hangingStart)
	requireFirstByteTimeout(t, err)
}

func TestFirstByteTimeout_SilentStream(t *testing.T) {
	result, err := executeStreamWithFirstByteTimeout(context.Background(), testFirstByteLimit, func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
		ch := make(chan cliproxyexecutor.StreamChunk)
		go func() {
			defer close(ch)
			<-ctx.Done()
			ch <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
		}()
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got []cliproxyexecutor.StreamChunk
	for chunk := range result.Chunks {
		got = append(got, chunk)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want one timeout error", len(got))
	}
	requireFirstByteTimeout(t, got[0].Err)
}

func TestFirstByteTimeout_LiftedAfterFirstPayload(t *testing.T) {
	result, err := executeStreamWithFirstByteTimeout(context.Background(), testFirstByteLimit, func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
		ch := make(chan cliproxyexecutor.StreamChunk)
		go func() {
			defer close(ch)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
			select {
			case <-time.After(3 * testFirstByteLimit):
			case <-ctx.Done():
				ch <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
				return
			}
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte("late")}
		}()
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var payloads []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream cut after first payload: %v", chunk.Err)
		}
		payloads = append(payloads, string(chunk.Payload))
	}
	if len(payloads) != 2 || payloads[1] != "late" {
		t.Fatalf("payloads = %v, want [first late]", payloads)
	}
}

func TestFirstByteTimeout_DisabledPassesThrough(t *testing.T) {
	want := errors.New("upstream said no")
	_, err := executeStreamWithFirstByteTimeout(context.Background(), 0, func(context.Context) (*cliproxyexecutor.StreamResult, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want passthrough", err)
	}
}

func TestExecuteStream_HungCredentialFailsOver(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	cfg := &config.Config{}
	cfg.Streaming.FirstByteTimeoutSeconds = 1
	m.runtimeConfig.Store(cfg)
	m.SetRetryConfig(1, 0, 2)
	ids := registerOverloadAuths(t, m, 2)

	var mu sync.Mutex
	var order []string
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			mu.Lock()
			order = append(order, auth.ID)
			mu.Unlock()
			if auth.ID == ids[0] {
				return hangingStart(ctx)
			}
			return successStreamResult(), nil
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("hung credential was not failed over: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != ids[0] || order[1] != ids[1] {
		t.Fatalf("attempt order = %v, want [%s %s]", order, ids[0], ids[1])
	}
	if auth, ok := m.GetByID(ids[0]); !ok || auth.NextRetryAfter.IsZero() {
		t.Fatal("hung credential has no cooldown; the next request would wait on it again")
	}
}
