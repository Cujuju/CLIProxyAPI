package auth

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideAntigravityCLIWindow keeps the console CLI from opening a window when the
// proxy runs without a console (as the desktop app's child).
func hideAntigravityCLIWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
