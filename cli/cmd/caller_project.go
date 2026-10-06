package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// callerProjectError matches by shape, not a marker file, so Caller projects scaffolded before it existed match too.
func callerProjectError(dir, command string) error {
	if _, err := os.Stat(filepath.Join(dir, "agent-card.json")); err == nil {
		return nil
	}
	for _, language := range []string{"python", "node"} {
		script, runCmd := callerScript(language)
		src, err := os.ReadFile(filepath.Join(dir, script))
		if err != nil || !bytes.Contains(src, []byte("TaskClient")) {
			continue
		}
		return fmt.Errorf("this project calls agents; it is not an agent, so '%s' does not apply here.\n"+
			"Run the script directly: %s\n"+
			"To build an agent that others can call, run 'blocks init' and choose \"Build an agent that others can call\".",
			command, runCmd)
	}
	return nil
}
