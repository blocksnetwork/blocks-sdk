package cmd

import (
	"os"
	"strings"
	"testing"
)

// TestSelectDeployTargetNonTTY verifies the deploy picker returns no selection
// when stdin is not a terminal, so callers fall through to the "no target"
// error rather than silently picking the first registered target.
func TestSelectDeployTargetNonTTY(t *testing.T) {
	got, err := selectDeployTarget("")
	if err != nil {
		t.Fatalf("selectDeployTarget: %v", err)
	}
	if got != "" {
		t.Errorf("selectDeployTarget() = %q, want \"\" on non-terminal stdin", got)
	}
}

// TestInitNoArgsDoesNotHang guards the regression where `blocks init` with no
// args under a non-terminal stdin entered an interactive loop that spun on EOF.
// It must return promptly with an error (the agent name is required), never hang.
func TestInitNoArgsDoesNotHang(t *testing.T) {
	restoreCLIState(t)
	resetInitFlags()
	t.Cleanup(resetInitFlags)
	t.Chdir(t.TempDir())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	_, err = runRootCapturing(t, "init")
	if err == nil {
		t.Fatal("expected an error for `blocks init` with no args on non-terminal stdin")
	}
	if !strings.Contains(err.Error(), "agent name is required in non-interactive mode") {
		t.Errorf("error = %q, want the missing agent name reported", err.Error())
	}
}
