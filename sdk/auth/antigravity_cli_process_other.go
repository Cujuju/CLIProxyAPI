//go:build !windows

package auth

import (
	"os"
	"os/exec"
	"syscall"

	log "github.com/sirupsen/logrus"
)

// antigravityCLINiceness lowers the CLI's CPU priority (0 normal, 19 lowest),
// so a quota run never competes with the app but still finishes under load.
const antigravityCLINiceness = 10

// backgroundAntigravityCLI is a no-op: only Windows opens a console window.
func backgroundAntigravityCLI(*exec.Cmd) {}

// lowerAntigravityCLIPriority renices the started CLI.
func lowerAntigravityCLIPriority(p *os.Process) {
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, p.Pid, antigravityCLINiceness); err != nil {
		log.WithError(err).Debug("antigravity cli: lower priority")
	}
}
