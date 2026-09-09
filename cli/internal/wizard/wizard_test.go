package wizard

import (
	"bufio"
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
func runWithClosedStdin(t *testing.T, nameFromArgs, langFromFlag, modeFromFlag string) Config {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close() // reads from r will see EOF / non-TTY
	orig := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = orig
		r.Close()
	}()
	cfg, err := Run(nameFromArgs, langFromFlag, modeFromFlag)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return cfg
}

func TestWizardModeFlagSkipsPrompt(t *testing.T) {
	cfg := runWithClosedStdin(t, "my_consumer", "node", "consumer")
	if cfg.Mode != "consumer" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "consumer")
	}
	if cfg.Language != "node" {
		t.Errorf("Language = %q, want %q", cfg.Language, "node")
	}
}

func TestWizardSkipsProviderPromptsForConsumer(t *testing.T) {
	cfg := runWithClosedStdin(t, "my_consumer", "python", "consumer")
	// Provider-only prompts must be skipped - their zero values prove
	// Run() returned before the Concurrency prompt would have defaulted
	// them to 1.
	if cfg.Concurrency != 0 {
		t.Errorf("Concurrency = %d, want 0 (prompt should be skipped for consumer)", cfg.Concurrency)
	}
	if cfg.ExpectedInstances != 0 {
		t.Errorf("ExpectedInstances = %d, want 0 for consumer", cfg.ExpectedInstances)
	}
	if cfg.Streaming {
		t.Error("Streaming should be false for consumer")
	}
	if len(cfg.TaskKinds) != 0 {
		t.Errorf("TaskKinds = %v, want empty for consumer", cfg.TaskKinds)
	}
	if cfg.Docker {
		t.Error("Docker should be false for consumer")
	}
	// DisplayName prompt must be skipped for consumers.
	if cfg.DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty (prompt should be skipped for consumer)", cfg.DisplayName)
	}
	// Description prompt must be skipped; a sensible default is used so
	// pyproject.toml metadata stays valid.
	if cfg.Description != "my_consumer consumer" {
		t.Errorf("Description = %q, want %q", cfg.Description, "my_consumer consumer")
	}
}

func TestWizardProviderPromptsDefaultFilled(t *testing.T) {
	// Control: when Type is "provider", all prompts run and defaults fill.
	cfg := runWithClosedStdin(t, "my_provider", "python", "provider")
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

func TestModeFromIndex(t *testing.T) {
	if got := modeFromIndex(0); got != "provider" {
		t.Errorf("modeFromIndex(0) = %q, want %q", got, "provider")
	}
	if got := modeFromIndex(1); got != "consumer" {
		t.Errorf("modeFromIndex(1) = %q, want %q", got, "consumer")
	}
	// Defensive: any other value falls back to provider.
	if got := modeFromIndex(99); got != "provider" {
		t.Errorf("modeFromIndex(99) = %q, want %q", got, "provider")
	}
}

func TestWizardNonTTYDefaultsToProvider(t *testing.T) {
	// When no --mode flag is given and stdin is non-TTY,
	// InteractiveSelect returns the default index (0) and the wizard
	// picks "provider". This exercises the same mapping path that a
	// human pressing Enter without navigating the arrow keys would hit.
	cfg := runWithClosedStdin(t, "x", "python", "")
	if cfg.Mode != "provider" {
		t.Errorf("Mode = %q, want %q (default index 0)", cfg.Mode, "provider")
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

func TestModeLabelsSwitchOnEnterprise(t *testing.T) {
	if got, want := modeLabels(false), []string{"Provider", "Consumer"}; got[0] != want[0] || got[1] != want[1] {
		t.Errorf("network labels = %v, want %v", got, want)
	}
	if got, want := modeLabels(true), []string{"Connect agent", "Call agent"}; got[0] != want[0] || got[1] != want[1] {
		t.Errorf("enterprise labels = %v, want %v", got, want)
	}
}

func TestNormalizeModeCanonicalValues(t *testing.T) {
	// Unit test: verify that aliases normalize to the expected canonical values.

	// Test provider/connect-agent normalization
	providerNorm, providerOK := NormalizeMode("provider")
	connectNorm, connectOK := NormalizeMode("connect-agent")

	if !providerOK || !connectOK {
		t.Fatalf("Both provider and connect-agent should be valid")
	}

	if providerNorm != connectNorm {
		t.Errorf("provider and connect-agent should normalize to same value: got %q vs %q",
			providerNorm, connectNorm)
	}

	// Test consumer/call-agent normalization
	consumerNorm, consumerOK := NormalizeMode("consumer")
	callNorm, callOK := NormalizeMode("call-agent")

	if !consumerOK || !callOK {
		t.Fatalf("Both consumer and call-agent should be valid")
	}

	if consumerNorm != callNorm {
		t.Errorf("consumer and call-agent should normalize to same value: got %q vs %q",
			consumerNorm, callNorm)
	}

	// Verify the canonical values are what we expect
	if providerNorm != "provider" {
		t.Errorf("provider/connect-agent should normalize to 'provider', got %q", providerNorm)
	}

	if consumerNorm != "consumer" {
		t.Errorf("consumer/call-agent should normalize to 'consumer', got %q", consumerNorm)
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
	// Test that readConfirm errors with --no-input instead of reading from stdin
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
	_, err = readConfirm(reader, "Enable streaming?", false, "help text")

	if err == nil {
		t.Error("readConfirm should return error with --no-input")
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

// Help, blank answers, and answers the validator rejects all re-prompt; only a
// valid answer is returned.
func TestReadRequiredLineRepromptsUntilValid(t *testing.T) {
	SetNoInputMode(false)
	t.Cleanup(func() { SetNoInputMode(false) })

	in := bufio.NewReader(strings.NewReader("?\n\nnot a name\ngood_name\n"))
	got, err := readRequiredLine(in, "Agent name", agentNameFlagHint, "help", ValidateAgentName)
	if err != nil {
		t.Fatalf("readRequiredLine: %v", err)
	}
	if got != "good_name" {
		t.Errorf("readRequiredLine = %q, want %q", got, "good_name")
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

	// readConfirm should return default on EOF
	confirm, err := readConfirm(reader, "Enable feature?", true, "help")
	if err != nil {
		t.Errorf("readConfirm should not error without --no-input: %v", err)
	}
	if !confirm {
		t.Error("readConfirm should return default (true) on EOF")
	}
}

func TestWizardFunctionsNonTTYBehaviorUnchanged(t *testing.T) {
	// Test that non-TTY without --no-input gives existing defaults without new errors
	SetNoInputMode(false)
	t.Cleanup(func() { SetNoInputMode(false) })

	// This test verifies that the existing non-TTY behavior remains unchanged
	cfg := runWithClosedStdin(t, "test_agent", "python", "provider")

	// These should still work as before - the changes should only affect --no-input mode
	if cfg.Name != "test_agent" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test_agent")
	}
	if cfg.Concurrency != 1 {
		t.Errorf("Concurrency = %d, want 1 (default should still work)", cfg.Concurrency)
	}
}
