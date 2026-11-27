//go:build !windows

package auth

import "os/exec"

// hideAntigravityCLIWindow is a no-op: only Windows opens a console window.
func hideAntigravityCLIWindow(*exec.Cmd) {}
