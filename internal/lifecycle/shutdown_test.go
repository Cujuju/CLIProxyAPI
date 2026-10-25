package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// failAfter bounds waits on background goroutines; it is a failure deadline,
// not a timing assumption.
const failAfter = 30 * time.Second

func TestWatchParentProcessRequestsShutdownWhenParentExits(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^$")
	if errStart := child.Start(); errStart != nil {
		t.Fatal(errStart)
	}
	ctx, cancel := WithShutdown(context.Background())
	defer cancel()
	WatchParentProcess(ctx, child.Process.Pid)
	if errWait := child.Wait(); errWait != nil {
		t.Fatal(errWait)
	}
	select {
	case <-ShutdownRequested():
	case <-time.After(failAfter):
		t.Fatal("parent exit did not request shutdown")
	}
	select {
	case <-ctx.Done():
	case <-time.After(failAfter):
		t.Fatal("shutdown did not cancel the service context")
	}
	RequestShutdown() // A second request must not panic.
}

func TestWatchParentProcessIgnoresInvalidPID(t *testing.T) {
	WatchParentProcess(context.Background(), 0)
	WatchParentProcess(context.Background(), -1)
}
