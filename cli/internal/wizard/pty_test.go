//go:build darwin || linux

package wizard

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Each prompt runs in this binary re-executed on a real pty, so raw mode, echo and Ctrl+C's SIGINT behave as for a user.

const (
	ptyScenarioEnv = "BLOCKS_WIZARD_PTY_SCENARIO"
	ptyHelpText    = "PTY-HELP-TEXT"
	ptyResult      = "RESULT="
	ptyTimeout     = 5 * time.Second
)

var ptyScenarios = map[string]func() (string, error){
	"menu": func() (string, error) {
		i, err := InteractiveSelect("Pick one", []string{"alpha", "beta"}, 0, ptyHelpText)
		return strconv.Itoa(i), err
	},
	"required": func() (string, error) {
		return readRequiredLine(bufio.NewReader(os.Stdin), "Agent name", agentNameFlagHint, ptyHelpText, ValidateAgentName)
	},
	"text": func() (string, error) {
		return readLine(bufio.NewReader(os.Stdin), "Display name", "dflt", ptyHelpText)
	},
	"numeric": func() (string, error) {
		n, err := readInt(bufio.NewReader(os.Stdin), "Expected instances", 1, minExpectedInstances, ptyHelpText)
		return strconv.Itoa(n), err
	},
	"boolean": func() (string, error) {
		ok, err := Confirm(bufio.NewReader(os.Stdin), "Enable streaming?", false, ptyHelpText)
		return strconv.FormatBool(ok), err
	},
	"final-confirm": func() (string, error) {
		ok, err := Confirm(bufio.NewReader(os.Stdin), "Continue?", true, ptyHelpText)
		return strconv.FormatBool(ok), err
	},
	"raw-confirm": func() (string, error) {
		ri, ok := newRawInput()
		if !ok {
			return "", errors.New("stdin is not a terminal")
		}
		defer ri.close()
		yes, err := ri.confirm("Add another agent?", false, ptyHelpText)
		return strconv.FormatBool(yes), err
	},
	"autocomplete": func() (string, error) {
		ri, ok := newRawInput()
		if !ok {
			return "", errors.New("stdin is not a terminal")
		}
		defer ri.close()
		return ri.autocomplete(context.Background(), "Agent to use", ptyHelpText, nil,
			func(v string, _ bool) error { return ValidateAgentName(v) }, nil)
	},
}

func TestPtyHelperProcess(t *testing.T) {
	name := os.Getenv(ptyScenarioEnv)
	if name == "" {
		t.Skip("helper process for the pty prompt tests")
	}
	got, err := ptyScenarios[name]()
	if err != nil {
		fmt.Printf("\r\nERR=%v\r\n", err)
		os.Exit(2)
	}
	fmt.Printf("\r\n%s%s\r\n", ptyResult, got)
	os.Exit(0)
}

type ptySession struct {
	t    *testing.T
	pty  *os.File
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	seen int
	done chan error
}

func startPtyScenario(t *testing.T, scenario string) *ptySession {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPtyHelperProcess$")
	cmd.Env = append(os.Environ(), ptyScenarioEnv+"="+scenario)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 160})
	if err != nil {
		t.Fatalf("start pty: %v", err)
	}
	s := &ptySession{t: t, pty: f, cmd: cmd, done: make(chan error, 1)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() { s.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = f.Close()
	})
	return s
}

func (s *ptySession) send(keys string) {
	s.t.Helper()
	if _, err := s.pty.Write([]byte(keys)); err != nil {
		s.t.Fatalf("write %q: %v", keys, err)
	}
}

// expect consumes output up to want, so a prompt redisplayed after help is a fresh match.
func (s *ptySession) expect(want string) {
	s.t.Helper()
	deadline := time.Now().Add(ptyTimeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		rest := s.out.String()[s.seen:]
		if i := strings.Index(rest, want); i >= 0 {
			s.seen += i + len(want)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %q; output so far:\n%q", want, s.output())
}

func (s *ptySession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

func (s *ptySession) wait() error {
	s.t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(ptyTimeout):
		s.t.Fatalf("prompt did not exit; output so far:\n%q", s.output())
		return nil
	}
}

type ptyPromptCase struct {
	scenario     string
	prompt       string
	helpKeys     string
	valid        string
	validOut     string
	enterOut     string // empty: Enter has no default and must re-prompt with enterMsg
	enterMsg     string
	invalid      string
	invalidMsg   string // empty: the raw prompt ignores the key silently
	clearInvalid string
}

var ptyPromptCases = []ptyPromptCase{
	{scenario: "menu", prompt: "Pick one", helpKeys: "?", valid: "\x1b[B\r", validOut: "1", enterOut: "0", invalid: "x"},
	{scenario: "required", prompt: "Agent name (? for help)", helpKeys: "?my_agent\r", valid: "good_name\r", validOut: "good_name",
		enterMsg: "Agent name is required.", invalid: "bad name\r", invalidMsg: "Invalid:"},
	{scenario: "text", prompt: "Display name [dflt] (? for help)", helpKeys: "?What\r", valid: "Weather\r", validOut: "Weather", enterOut: "dflt"},
	{scenario: "numeric", prompt: "Expected instances [1] (? for help)", helpKeys: "?3\r", valid: "0\r", validOut: "0", enterOut: "1",
		invalid: "abc\r", invalidMsg: "Enter a whole number of 0 or more."},
	{scenario: "boolean", prompt: "Enable streaming? [y/N] (? for help)", helpKeys: "?y\r", valid: "y\r", validOut: "true", enterOut: "false",
		invalid: "maybe\r", invalidMsg: "Please answer y or n."},
	{scenario: "final-confirm", prompt: "Continue? [Y/n] (? for help)", helpKeys: "?\r", valid: "n\r", validOut: "false", enterOut: "true",
		invalid: "maybe\r", invalidMsg: "Please answer y or n."},
	{scenario: "raw-confirm", prompt: "Add another agent? [y/N] (? for help)", helpKeys: "?", valid: "y", validOut: "true", enterOut: "false",
		invalid: "x", invalidMsg: "Please answer y or n."},
	{scenario: "autocomplete", prompt: "Agent to use", helpKeys: "?", valid: "translator\r", validOut: "translator",
		enterMsg: "Enter an agent name", invalid: "bad-name\r", invalidMsg: "agentName must contain only",
		clearInvalid: strings.Repeat("\x7f", len("bad-name"))},
}

func TestPtyPromptsHonorHelpDefaultsValidationAndCtrlC(t *testing.T) {
	for _, tc := range ptyPromptCases {
		t.Run(tc.scenario+"/help then answer", func(t *testing.T) {
			t.Parallel()
			s := startPtyScenario(t, tc.scenario)
			s.expect(tc.prompt)
			s.send(tc.helpKeys)
			s.expect(ptyHelpText)
			s.expect(tc.prompt)
			s.send(tc.valid)
			s.expect(ptyResult + tc.validOut)
			if err := s.wait(); err != nil {
				t.Fatalf("exit: %v", err)
			}
		})

		t.Run(tc.scenario+"/enter", func(t *testing.T) {
			t.Parallel()
			s := startPtyScenario(t, tc.scenario)
			s.expect(tc.prompt)
			s.send("\r")
			if tc.enterOut == "" {
				s.expect(tc.enterMsg)
				s.send(tc.valid)
				s.expect(ptyResult + tc.validOut)
			} else {
				s.expect(ptyResult + tc.enterOut)
			}
			if err := s.wait(); err != nil {
				t.Fatalf("exit: %v", err)
			}
		})

		if tc.invalid != "" {
			t.Run(tc.scenario+"/invalid then answer", func(t *testing.T) {
				t.Parallel()
				s := startPtyScenario(t, tc.scenario)
				s.expect(tc.prompt)
				s.send(tc.invalid)
				if tc.invalidMsg != "" {
					s.expect(tc.invalidMsg)
				}
				s.send(tc.clearInvalid + tc.valid)
				s.expect(ptyResult + tc.validOut)
				if err := s.wait(); err != nil {
					t.Fatalf("exit: %v", err)
				}
			})
		}

		t.Run(tc.scenario+"/ctrl-c", func(t *testing.T) {
			t.Parallel()
			s := startPtyScenario(t, tc.scenario)
			s.expect(tc.prompt)
			s.send("\x03")
			if err := s.wait(); err == nil {
				t.Fatal("Ctrl+C must end the process with a failure status")
			}
			if strings.Contains(s.output(), ptyResult) {
				t.Errorf("Ctrl+C must not submit an answer:\n%q", s.output())
			}
		})
	}
}

func TestPtyMenuEscCancels(t *testing.T) {
	s := startPtyScenario(t, "menu")
	s.expect("Pick one")
	s.send("\x1b")
	s.expect("ERR=" + ErrCanceled.Error())
	if err := s.wait(); err == nil {
		t.Fatal("a canceled menu must exit with a failure status")
	}
}
