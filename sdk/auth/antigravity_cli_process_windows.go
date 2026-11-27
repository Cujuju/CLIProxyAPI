package auth

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// backgroundAntigravityCLI starts the CLI without a console window (the proxy
// runs as the desktop app's child) and below normal CPU priority, so a quota
// run never competes with the app.
func backgroundAntigravityCLI(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.BELOW_NORMAL_PRIORITY_CLASS,
	}
}

// lowerAntigravityCLIPriority is a no-op: Windows sets the priority at creation.
func lowerAntigravityCLIPriority(*os.Process) {}
