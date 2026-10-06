package wizard

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("myagent")

	if cfg.Name != "myagent" {
		t.Errorf("Name = %q, want %q", cfg.Name, "myagent")
	}
	if cfg.DisplayName != "myagent" {
		t.Errorf("DisplayName = %q, want %q (should default to Name)", cfg.DisplayName, "myagent")
	}
	if cfg.Description != "myagent agent" {
		t.Errorf("Description = %q, want %q", cfg.Description, "myagent agent")
	}
	if cfg.Language != "python" {
		t.Errorf("Language = %q, want %q", cfg.Language, "python")
	}
	if cfg.Concurrency != 1 {
		t.Errorf("Concurrency = %d, want 1", cfg.Concurrency)
	}
	if cfg.ExpectedInstances != 1 {
		t.Errorf("ExpectedInstances = %d, want 1", cfg.ExpectedInstances)
	}
	if cfg.Streaming {
		t.Error("Streaming should be false by default")
	}
	if len(cfg.TaskKinds) != 1 || cfg.TaskKinds[0] != "request" {
		t.Errorf("TaskKinds = %v, want [request]", cfg.TaskKinds)
	}
	if cfg.Docker {
		t.Error("Docker should be false by default")
	}
}

func TestDefaultConfigDifferentNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"foo", "foo agent"},
		{"my_cool_bot", "my_cool_bot agent"},
		{"x", "x agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(tt.name)
			if cfg.Description != tt.want {
				t.Errorf("DefaultConfig(%q).Description = %q, want %q", tt.name, cfg.Description, tt.want)
			}
			if cfg.DisplayName != tt.name {
				t.Errorf("DefaultConfig(%q).DisplayName = %q, want %q", tt.name, cfg.DisplayName, tt.name)
			}
		})
	}
}

func TestValidateAgentName(t *testing.T) {
	valid := []string{"echo", "acme_echo", "my_agent_v2", "BnplAgent", "a", "A_b_c123"}
	for _, name := range valid {
		if err := ValidateAgentName(name); err != nil {
			t.Errorf("ValidateAgentName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{"acme.echo", "my agent", "foo/bar", "agent@v2", "", "a b", "acme-echo"}
	for _, name := range invalid {
		err := ValidateAgentName(name)
		if err == nil {
			t.Errorf("ValidateAgentName(%q) = nil, want error", name)
		} else if !strings.Contains(err.Error(), "alphanumeric") {
			t.Errorf("ValidateAgentName(%q) error = %q, want message about alphanumeric", name, err)
		}
	}
}

func TestDefaultConfigModeProvider(t *testing.T) {
	cfg := DefaultConfig("myagent")
	if cfg.Mode != "provider" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "provider")
	}
}

// runWithClosedStdin redirects os.Stdin to a closed pipe, so every prompt
// returns its default immediately (InteractiveSelect returns defaultIdx
// because IsTerminal returns false; readLine returns defaultVal on EOF).
func runWithClosedStdin(t *testing.T, nameFromArgs, langFromFlag string) Config {
	t.Helper()
	closeStdin(t)
	cfg, err := Run(nameFromArgs, langFromFlag)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return cfg
}

func closeStdin(t *testing.T) {
	t.Helper()
	stdinWith(t, "")
}

func stdinWith(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	w.Close() // reads from r will see EOF / non-TTY
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		r.Close()
	})
}

func TestWizardProviderPromptsDefaultFilled(t *testing.T) {
	// Control: when Type is "provider", all prompts run and defaults fill.
	cfg := runWithClosedStdin(t, "my_provider", "python")
	if cfg.Concurrency != 1 {
		t.Errorf("Concurrency = %d, want 1 (readInt default)", cfg.Concurrency)
	}
	if cfg.ExpectedInstances != 1 {
		t.Errorf("ExpectedInstances = %d, want 1", cfg.ExpectedInstances)
	}
	if len(cfg.TaskKinds) == 0 {
		t.Error("TaskKinds should be non-empty after provider prompts run")
	}
}

func TestRunCallerBlankAgentSkipsPick(t *testing.T) {
	stdinWith(t, "\n")
	cfg, err := RunCaller(context.Background(), CallerOptions{Name: "my-caller", Language: "python"})
	if err != nil {
		t.Fatalf("RunCaller: %v", err)
	}
	if cfg.TargetAgent != "" {
		t.Errorf("TargetAgent = %q, want empty after a skipped pick", cfg.TargetAgent)
	}
}

func TestRunCallerRejectsUnsafeProjectName(t *testing.T) {
	for _, name := range []string{"../escape", ".caller", "caller-"} {
		closeStdin(t)
		if _, err := RunCaller(context.Background(), CallerOptions{Name: name, Language: "python"}); err == nil {
			t.Errorf("RunCaller accepted unsafe project name %q", name)
		}
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"provider", "provider", true},
		{"consumer", "consumer", true},
		{"connect-agent", "provider", true},
		{"call-agent", "consumer", true},
		{"Connect-Agent", "provider", true}, // case-insensitive
		{"  provider  ", "provider", true},  // trimmed
		{"webapp", "", false},               // not a mode
		{"", "", false},
		{"nonsense", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeMode(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("NormalizeMode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// Tests for --no-input functionality

func TestReadLineNoInputMode(t *testing.T) {
	// Test that readLine errors with --no-input instead of reading from stdin
	SetNoInputMode(true)
	t.Cleanup(func() { SetNoInputMode(false) })

	// Use a pipe we can monitor - if readLine incorrectly tries to read,
	// we'll detect it by checking if the pipe was touched
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	// Write something to the pipe so we can detect if it gets read
	go func() {
		w.Write([]byte("this should not be read\n"))
		w.Close()
	}()

	reader := bufio.NewReader(r)
	_, err = readLine(reader, "Display name", "default", "help text")

	if err == nil {
		t.Error("readLine should return error with --no-input")
	}
	if !strings.Contains(err.Error(), "cannot ask \"Display name\" with --no-input") {
		t.Errorf("error should mention the question name, got: %v", err)
	}

	// Verify the pipe wasn't read from by attempting a non-blocking read
	// If readLine correctly refused to read, the data should still be there
	buf := make([]byte, 100)
	n, _ := r.Read(buf)
	if n == 0 {
		t.Error("readLine should not have read from stdin with --no-input, but the pipe appears to have been drained")
	}
}

func TestReadConfirmNoInputMode(t *testing.T) {
	// Test that Confirm errors with --no-input instead of reading from stdin
	SetNoInputMode(true)
	t.Cleanup(func() { SetNoInputMode(false) })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	w.Close() // Close write end immediately

	reader := bufio.NewReader(r)
	_, err = Confirm(reader, "Enable streaming?", false, "help text")

	if err == nil {
		t.Error("Confirm should return error with --no-input")
	}
	if !strings.Contains(err.Error(), "cannot ask \"Enable streaming?\" with --no-input") {
		t.Errorf("error should mention the question name, got: %v", err)
	}
}

func TestReadIntNoInputMode(t *testing.T) {
	// Test similar to readLine for readInt
	SetNoInputMode(true)
	t.Cleanup(func() { SetNoInputMode(false) })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	w.Close()

	reader := bufio.NewReader(r)
	_, err = readInt(reader, "Max concurrent tasks", 1, 1, "help text")

	if err == nil {
		t.Error("readInt should return error with --no-input")
	}
	if !strings.Contains(err.Error(), "cannot ask \"Max concurrent tasks\" with --no-input") {
		t.Errorf("error should mention the question name, got: %v", err)
	}
}

// The name prompt has no default to fall back on, so it is the one prompt that
// could not reuse readLine. It must still refuse to read under --no-input, and
// say which argument answers it — the name is supplied positionally, not by a
// flag with a value.
func TestReadRequiredLineNoInputMode(t *testing.T) {
	SetNoInputMode(true)
	t.Cleanup(func() { SetNoInputMode(false) })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := w.Write([]byte("this should not be read\n")); err != nil {
		t.Fatal(err)
	}

	_, err = readRequiredLine(bufio.NewReader(r), "Agent name", agentNameFlagHint, "help text", ValidateAgentName)
	if err == nil {
		t.Fatal("readRequiredLine should return an error with --no-input")
	}
	if !strings.Contains(err.Error(), "cannot ask \"Agent name\" with --no-input") {
		t.Errorf("error should name the question, got: %v", err)
	}
	if !strings.Contains(err.Error(), "blocks init <name>") {
		t.Errorf("error should name what supplies the value instead, got: %v", err)
	}

	buf := make([]byte, 100)
	if n, _ := r.Read(buf); n == 0 {
		t.Error("readRequiredLine drained stdin despite --no-input")
	}
}

// A closed stdin must end the loop with an error rather than re-prompting
// forever: there is no default to accept and no further input coming.
func TestReadRequiredLineEOFIsAnError(t *testing.T) {
	SetNoInputMode(false)
	t.Cleanup(func() { SetNoInputMode(false) })

	_, err := readRequiredLine(bufio.NewReader(strings.NewReader("")), "Agent name", agentNameFlagHint, "help", ValidateAgentName)
	if err == nil {
		t.Fatal("readRequiredLine should error on EOF instead of spinning or inventing a value")
	}
	if !strings.Contains(err.Error(), "Agent name is required") {
		t.Errorf("error should say the value is required, got: %v", err)
	}
	if !strings.Contains(err.Error(), "blocks init <name>") {
		t.Errorf("error should name what supplies the value instead, got: %v", err)
	}
}

func TestWizardFunctionsNormalBehaviorWithoutNoInput(t *testing.T) {
	// Test that without --no-input, all functions maintain current behavior
	SetNoInputMode(false) // Ensure it's off
	t.Cleanup(func() { SetNoInputMode(false) })

	// Test with closed stdin (non-TTY behavior) - should return defaults
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.Close() // EOF on read

	reader := bufio.NewReader(r)

	// readLine should return default on EOF
	line, err := readLine(reader, "Display name", "default_name", "help")
	if err != nil {
		t.Errorf("readLine should not error without --no-input: %v", err)
	}
	if line != "default_name" {
		t.Errorf("readLine should return default on EOF, got %q", line)
	}

	// readInt should return default on EOF
	num, err := readInt(reader, "Concurrency", 5, 1, "help")
	if err != nil {
		t.Errorf("readInt should not error without --no-input: %v", err)
	}
	if num != 5 {
		t.Errorf("readInt should return default on EOF, got %d", num)
	}

	// Confirm should return default on EOF
	confirm, err := Confirm(reader, "Enable feature?", true, "help")
	if err != nil {
		t.Errorf("Confirm should not error without --no-input: %v", err)
	}
	if !confirm {
		t.Error("Confirm should return default (true) on EOF")
	}
}
