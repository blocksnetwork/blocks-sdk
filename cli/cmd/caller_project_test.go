package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/scaffold"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

func TestInitCallerFlagPathWritesChosenAgent(t *testing.T) {
	out, err := runInitWithoutTerminal(t, "my-caller", "--mode", "consumer", "--agent", "translator", "--yes")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	assertCallsAgent(t, filepath.Join("my-caller", "main.py"), "translator")
	if !strings.Contains(out, "python main.py") || strings.Contains(out, "blocks register") {
		t.Errorf("next steps must run the script, not register an agent:\n%s", out)
	}
}

// The Caller wizard's agent pick is its last stdin read, so no line-mode confirmation may follow it.
func TestInitCallerWizardScaffoldsWithoutFinalConfirmation(t *testing.T) {
	out, dir, err := runInitArgs(t, "", "my-caller", "--mode", "consumer", "--agent", "translator", "--language", "python")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.Contains(out, "Continue?") {
		t.Errorf("the Caller wizard asked the line-mode confirmation:\n%s", out)
	}
	assertCallsAgent(t, filepath.Join(dir, "my-caller", "main.py"), "translator")
}

func assertCallsAgent(t *testing.T, script, agent string) {
	t.Helper()
	src, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if want := `AGENT_NAME = "` + agent + `"`; !strings.Contains(string(src), want) {
		t.Errorf("%s does not call the chosen agent; want %q in:\n%s", script, want, src)
	}
}

func TestInitCallerRejectsMoreThanOneAgent(t *testing.T) {
	_, err := runInitWithoutTerminal(t, "my-caller", "--mode", "consumer", "--agent", "a", "--agent", "b", "--yes")
	if err == nil || !strings.Contains(err.Error(), "pass --agent once") {
		t.Fatalf("want the one-agent error, got: %v", err)
	}
}

func TestPrintNextSteps_CallerWithoutAgentPointsAtDiscovery(t *testing.T) {
	inNetworkContext(t)
	out := captureStdout(func() {
		printNextSteps(wizard.Config{Name: "my-caller", Mode: "consumer", Language: "python"})
	})
	if !strings.Contains(out, "Set AGENT_NAME in main.py") || !strings.Contains(out, callerDiscoveryHint) {
		t.Errorf("next steps do not explain how to pick an agent:\n%s", out)
	}
	if strings.Contains(out, "blocks register") {
		t.Errorf("next steps send a Caller to register an agent:\n%s", out)
	}
}

func TestAgentOnlyCommandsExplainCallerProject(t *testing.T) {
	for _, command := range []string{"run", "check", "register", "publish"} {
		t.Run(command, func(t *testing.T) {
			chdirToScaffold(t, wizard.DefaultCallerConfig("my-caller"))
			err := executeInProject(command)
			if err == nil || !strings.Contains(err.Error(), "this project calls agents") || !strings.Contains(err.Error(), "python main.py") {
				t.Fatalf("blocks %s: want the Caller-project explanation, got: %v", command, err)
			}
		})
	}
}

func TestCheckTreatsAgentProjectWithTaskClientScriptAsAgent(t *testing.T) {
	chdirToScaffold(t, wizard.DefaultConfig("my_agent"))
	if err := os.WriteFile("main.py", []byte("from blocks_network import TaskClient\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := executeInProject("check"); err != nil {
		t.Errorf("blocks check in an agent project: %v", err)
	}
}

func chdirToScaffold(t *testing.T, cfg wizard.Config) {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateAmbientState(t)
	resetNoInput(t)
	dir := filepath.Join(t.TempDir(), cfg.Name)
	if err := scaffold.Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func executeInProject(command string) error {
	var err error
	captureStdoutStderr(func() {
		rootCmd.SetArgs([]string{command})
		err = rootCmd.Execute()
	})
	return err
}
