// Package lifecycle lets in-process triggers stop the proxy the same way
// SIGINT does. Desktop supervisors use it through the management API and the
// -parent-pid flag, because Windows offers no portable Ctrl+C to a child.
package lifecycle

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ParentPollInterval bounds how long the proxy outlives a crashed parent on
// platforms that must poll for process exit.
const ParentPollInterval = 2 * time.Second

var (
	shutdownOnce sync.Once
	shutdownCh   = make(chan struct{})
)

// RequestShutdown asks the running service to stop gracefully. It is safe to
// call more than once.
func RequestShutdown() {
	shutdownOnce.Do(func() {
		log.Info("graceful shutdown requested")
		close(shutdownCh)
	})
}

// ShutdownRequested is closed once RequestShutdown has been called.
func ShutdownRequested() <-chan struct{} {
	return shutdownCh
}

// WithShutdown returns a context cancelled when parent ends or a shutdown is
// requested.
func WithShutdown(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-shutdownCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// WatchParentProcess requests shutdown once process pid exits. It returns
// immediately; watching happens in the background until ctx ends.
func WatchParentProcess(ctx context.Context, pid int) {
	if pid <= 0 {
		return
	}
	go func() {
		if errWait := waitForProcessExit(ctx, pid); errWait != nil {
			log.Warnf("parent process watch for pid %d stopped: %v", pid, errWait)
			return
		}
		if ctx.Err() != nil {
			return
		}
		log.Infof("parent process %d exited; shutting down", pid)
		RequestShutdown()
	}()
}
