package wizard

import (
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
