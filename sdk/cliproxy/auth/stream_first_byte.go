package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ErrorCodeFirstByteTimeout marks a stream attempt whose upstream sent nothing within
// requests.streaming.first-byte-timeout-seconds.
const ErrorCodeFirstByteTimeout = "upstream_first_byte_timeout"

// errFirstByteDeadline is the cancel cause of an attempt context whose upstream stayed silent.
var errFirstByteDeadline = errors.New("upstream first byte deadline")

// newFirstByteTimeoutError is a retryable 504: the caller tries the next model or credential,
// and the transient-error cooldown keeps the next request off the hung credential.
func newFirstByteTimeoutError(limit time.Duration) *Error {
	return &Error{
		Code:       ErrorCodeFirstByteTimeout,
		Message:    fmt.Sprintf("upstream sent no stream data within %s", limit),
		Retryable:  true,
		HTTPStatus: http.StatusGatewayTimeout,
	}
}

// streamFirstByteTimeout returns the configured bound, or 0 when disabled.
func (m *Manager) streamFirstByteTimeout() time.Duration {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || cfg.Streaming.FirstByteTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.Streaming.FirstByteTimeoutSeconds) * time.Second
}

// executeStreamAttempt starts one upstream stream. With a first-byte bound, the attempt fails with
// newFirstByteTimeoutError unless the first payload or upstream error arrives in time. The bound
// is lifted at the first payload, so long streams are never cut.
func (m *Manager) executeStreamAttempt(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return executeStreamWithFirstByteTimeout(ctx, m.streamFirstByteTimeout(), func(attemptCtx context.Context) (*cliproxyexecutor.StreamResult, error) {
		return executor.ExecuteStream(attemptCtx, auth, req, opts)
	})
}

func executeStreamWithFirstByteTimeout(ctx context.Context, limit time.Duration, start func(context.Context) (*cliproxyexecutor.StreamResult, error)) (*cliproxyexecutor.StreamResult, error) {
	if limit <= 0 {
		return start(ctx)
	}
	attemptCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(limit, func() { cancel(errFirstByteDeadline) })
	timedOut := func() bool { return errors.Is(context.Cause(attemptCtx), errFirstByteDeadline) }

	result, err := start(attemptCtx)
	if err != nil || result == nil || result.Chunks == nil {
		timer.Stop()
		fired := timedOut()
		cancel(nil)
		if fired {
			return nil, newFirstByteTimeoutError(limit)
		}
		return result, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go forwardFirstByteGuarded(ctx, result.Chunks, out, timer, timedOut, func() { cancel(nil) }, limit)
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}, nil
}

// forwardFirstByteGuarded relays chunks, replacing the upstream outcome with a timeout error when
// the deadline fired before the first payload. It stops relaying when the caller's context ends.
func forwardFirstByteGuarded(ctx context.Context, in <-chan cliproxyexecutor.StreamChunk, out chan<- cliproxyexecutor.StreamChunk, timer *time.Timer, timedOut func() bool, release func(), limit time.Duration) {
	defer close(out)
	defer release()
	send := func(chunk cliproxyexecutor.StreamChunk) bool {
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			discardStreamChunks(in)
			return false
		}
	}
	armed := true
	for chunk := range in {
		if armed && (chunk.Err != nil || len(chunk.Payload) > 0) {
			armed = false
			timer.Stop()
			if timedOut() {
				discardStreamChunks(in)
				send(cliproxyexecutor.StreamChunk{Err: newFirstByteTimeoutError(limit)})
				return
			}
		}
		if !send(chunk) {
			return
		}
	}
	if armed {
		timer.Stop()
		if timedOut() {
			send(cliproxyexecutor.StreamChunk{Err: newFirstByteTimeoutError(limit)})
		}
	}
}
