package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

func TestInitWizardOpensWithPromptIntro(t *testing.T) {
	out, _, err := runInitArgs(t, "\n", "my_agent", "--mode", "provider", "--language", "python")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if wizard.PromptIntro != "Type ? at any prompt for an explanation. Press Enter to accept the value in brackets." {
		t.Errorf("PromptIntro drifted from the agreed wording: %q", wizard.PromptIntro)
	}
	intro, ask := strings.Index(out, wizard.PromptIntro), strings.Index(out, "Continue?")
	if intro < 0 || ask < 0 || intro > ask {
		t.Errorf("intro must print once before the first prompt:\n%s", out)
	}
	if strings.Count(out, wizard.PromptIntro) != 1 {
		t.Errorf("intro printed %d times, want 1:\n%s", strings.Count(out, wizard.PromptIntro), out)
	}
}

// Called directly: every init path that reaches the confirmation runs the wizard first, whose buffered reader drains piped stdin.
func TestInitFinalConfirmationTreatsQuestionMarkAsHelp(t *testing.T) {
	restoreCLIState(t)
	resetNoInput(t)
	pipeStdin(t, "?\nn\n")
	var err error
	out := captureStdout(func() { err = confirmScaffold() })
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("want canceled after ? then n, got: %v", err)
	}
	if !strings.Contains(out, "Continue? [Y/n] (? for help)") {
		t.Errorf("final confirmation must advertise help:\n%s", out)
	}
	if !strings.Contains(out, helpConfirmScaffold) {
		t.Errorf("? must print the confirmation help:\n%s", out)
	}
}

func runInitWithoutTerminal(t *testing.T, args ...string) (string, error) {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateAmbientState(t)
	t.Setenv("BLOCKS_CDM_URL", "")
	cdm.Reset()
	isTTY = func() bool { return false }
	isInteractive = func() bool { return false }
	resetNoInput(t)
	pipeStdin(t, "")
	resetInitFlags()
	t.Cleanup(resetInitFlags)
	t.Chdir(t.TempDir())

	var err error
	out := captureStdoutStderr(func() {
		rootCmd.SetArgs(append([]string{"init"}, args...))
		err = rootCmd.Execute()
	})
	return out, err
}

func TestInitWithoutTerminalSaysItIsNotPrompting(t *testing.T) {
	out, err := runInitWithoutTerminal(t, "my_agent")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "stdin is not a terminal") || !strings.Contains(out, "blocks init --help") {
		t.Errorf("missing the not-prompting notice:\n%s", out)
	}
}

func TestInitWithYesStaysQuietAboutPrompting(t *testing.T) {
	out, err := runInitWithoutTerminal(t, "my_agent", "--yes")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.Contains(out, "stdin is not a terminal") {
		t.Errorf("--yes asked for no prompts, so the notice is noise:\n%s", out)
	}
}

func TestLoginOfferQuestionMarkIsHelpNotConsent(t *testing.T) {
	restoreCLIState(t)
	resetNoInput(t)
	isTTY = func() bool { return true }
	pipeStdin(t, "?\nmaybe\nn\n")
	stdinScanner = nil

	var key string
	var err error
	out := captureStdout(func() {
		key, err = ensureOrOfferBlocksLogin(context.Background(), cardAccess{resolvedTarget: true})
	})
	if err != nil || key != "" {
		t.Fatalf("got %q, %v; want no login after ? and maybe then n", key, err)
	}
	if !strings.Contains(out, helpLoginOffer) || !strings.Contains(out, "Please answer y or n.") {
		t.Errorf("want the help and a re-prompt:\n%s", out)
	}
}

func TestWriteEnvQuestionMarkIsHelpNotConsent(t *testing.T) {
	restoreCLIState(t)
	resetNoInput(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	isInteractive = func() bool { return true }
	pipeStdin(t, "?\nmaybe\nn\n")
	stdinScanner = nil

	var write bool
	var err error
	out := captureStdout(func() { write, err = shouldWriteEnv() })
	if err != nil || write {
		t.Fatalf("got %v, %v; want no .env write after ? and maybe then n", write, err)
	}
	if !strings.Contains(out, helpWriteEnv) || !strings.Contains(out, "Please answer y or n.") {
		t.Errorf("want the help and a re-prompt:\n%s", out)
	}
}
