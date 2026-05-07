//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// sysExec replaces the current process with the given command (Unix exec).
func sysExec(binary string, argv []string, dir string) error {
	env := withCLIVersion(os.Environ())

	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("chdir: %w", err)
	}
	return syscall.Exec(binary, argv, env)
}
