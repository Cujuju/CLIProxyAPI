//go:build windows

package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// waitForProcessExit blocks until pid exits or ctx ends. A pid that no longer
// exists counts as exited; any other open failure stops the watch.
func waitForProcessExit(ctx context.Context, pid int) error {
	handle, errOpen := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(errOpen, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if errOpen != nil {
		return fmt.Errorf("open parent process: %w", errOpen)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	waitMillis := uint32(ParentPollInterval.Milliseconds())
	for {
		event, errWait := windows.WaitForSingleObject(handle, waitMillis)
		if errWait != nil {
			return fmt.Errorf("wait for parent process: %w", errWait)
		}
		if event == windows.WAIT_OBJECT_0 {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}
