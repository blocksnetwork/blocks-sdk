//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

// sysExec runs the command as a subprocess on Windows (syscall.Exec is not available).
func sysExec(binary string, argv []string, dir string) error {
	cmd := exec.Command(binary, argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = withCLIVersion(os.Environ())

	// Forward interrupt signals to the child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	go func() {
		for range sigCh {
			// Signal is automatically forwarded to the child on Windows
		}
	}()

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}
