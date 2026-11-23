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
	ctx = m.observeStreamQuota(ctx, auth, opts.Headers)
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

	type startOutcome struct {
		result   *cliproxyexecutor.StreamResult
		err      error
		panicked bool
		panicVal any
	}
	started := make(chan startOutcome)
	abandoned := make(chan struct{})
	go func() {
		var outcome startOutcome
		func() {
			defer func() {
				if r := recover(); r != nil {
					outcome.panicked = true
					outcome.panicVal = r
				}
			}()
			outcome.result, outcome.err = start(attemptCtx)
		}()
		select {
		case started <- outcome:
		case <-abandoned:
			if outcome.result != nil && outcome.result.Chunks != nil {
				discardStreamChunks(outcome.result.Chunks)
			}
			cancel(nil)
		}
	}()

	var result *cliproxyexecutor.StreamResult
	var err error
	select {
	case out := <-started:
		if out.panicked {
			timer.Stop()
			cancel(nil)
			panic(out.panicVal)
		}
		result, err = out.result, out.err
	case <-attemptCtx.Done():
		timer.Stop()
		fired := timedOut()
		close(abandoned)
		if fired {
			return nil, newFirstByteTimeoutError(limit)
		}
		return nil, ctx.Err()
	}

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
	go forwardFirstByteGuarded(ctx, attemptCtx, result.Chunks, out, timer, timedOut, func() { cancel(nil) }, limit)
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}, nil
}

// forwardFirstByteGuarded relays chunks, replacing the upstream outcome with a timeout error when
// the deadline fired before the first payload. It stops relaying when the caller's context ends.
func forwardFirstByteGuarded(ctx, attemptCtx context.Context, in <-chan cliproxyexecutor.StreamChunk, out chan<- cliproxyexecutor.StreamChunk, timer *time.Timer, timedOut func() bool, release func(), limit time.Duration) {
	defer close(out)
	defer release()
	drained := false
	drainOnce := func() {
		if !drained {
			drained = true
			discardStreamChunks(in)
		}
	}
	send := func(chunk cliproxyexecutor.StreamChunk) bool {
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			drainOnce()
			return false
		}
	}
	armed := true
	for {
		var chunk cliproxyexecutor.StreamChunk
		var ok bool
		if armed {
			select {
			case chunk, ok = <-in:
			case <-attemptCtx.Done():
				timer.Stop()
				drainOnce()
				if timedOut() {
					send(cliproxyexecutor.StreamChunk{Err: newFirstByteTimeoutError(limit)})
				}
				return
			}
		} else {
			chunk, ok = <-in
		}
		if !ok {
			break
		}
		if armed && (chunk.Err != nil || len(chunk.Payload) > 0) {
			armed = false
			timer.Stop()
			if timedOut() {
				drainOnce()
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
