//go:build !windows

package lifecycle

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// waitForProcessExit polls pid until it exits or ctx ends.
func waitForProcessExit(ctx context.Context, pid int) error {
	ticker := time.NewTicker(ParentPollInterval)
	defer ticker.Stop()
	for {
		if errSignal := syscall.Kill(pid, 0); errSignal != nil && !errors.Is(errSignal, syscall.EPERM) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
