package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
)

// supervisorPasswordEnv carries the desktop supervisor's per-launch management password.
// It is an environment variable, not a flag, so it never appears in process listings.
const supervisorPasswordEnv = "MANAGEMENT_SUPERVISOR_PASSWORD"

// takeSupervisorPassword reads and clears the supervisor password, so plugins and child processes never inherit it.
func takeSupervisorPassword() (string, error) {
	value := strings.TrimSpace(os.Getenv(supervisorPasswordEnv))
	_ = os.Unsetenv(supervisorPasswordEnv)
	if value != "" && len(value) < management.MinSupervisorPasswordLength {
		return "", fmt.Errorf("%s must be at least %d characters", supervisorPasswordEnv, management.MinSupervisorPasswordLength)
	}
	return value, nil
}
