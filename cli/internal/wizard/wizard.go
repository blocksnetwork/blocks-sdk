package wizard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var agentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// Config holds all wizard answers needed to scaffold a project.
type Config struct {
	Name              string   // machine identifier (agentName)
	DisplayName       string   // human-readable display name
	Description       string
	Language          string   // "node" or "python"
	Type              string   // "provider" (default) or "consumer"
	Concurrency       int
	ExpectedInstances int
	Streaming         bool
	TaskKinds         []string // "request", "pipe", or both
	Docker            bool
}

// ValidateAgentName checks that name matches /^[a-zA-Z0-9_]+$/.
func ValidateAgentName(name string) error {
	if !agentNameRe.MatchString(name) {
		return fmt.Errorf("agentName must contain only alphanumeric characters and underscores")
	}
	return nil
}

// DefaultConfig returns the default non-interactive configuration.
// DisplayName defaults to the agentName (Name).
func DefaultConfig(name string) Config {
	return Config{
		Name:              name,
		DisplayName:       name,
		Description:       name + " agent",
		Language:          "python",
		Type:              "provider",
		Concurrency:       1,
		ExpectedInstances: 1,
		Streaming:         false,
		TaskKinds:         []string{"request"},
		Docker:            false,
	}
}

// Run executes the interactive wizard, prompting the user for project config.
// If nameFromArgs is non-empty, the name prompt is skipped.
// If langFromFlag is non-empty ("node" or "python"), the language prompt is skipped.
// If typeFromFlag is non-empty ("provider" or "consumer"), the type prompt is skipped.
func Run(nameFromArgs string, langFromFlag string, typeFromFlag string) (Config, error) {
	r := bufio.NewReader(os.Stdin)
	var cfg Config

	// Name
	if nameFromArgs != "" {
		if err := ValidateAgentName(nameFromArgs); err != nil {
			return cfg, err
		}
		cfg.Name = nameFromArgs
	} else {
		name, err := readLine(r, "Agent name", "")
		if err != nil {
			return cfg, err
		}
		if strings.TrimSpace(name) == "" {
			return cfg, fmt.Errorf("name is required")
		}
		if err := ValidateAgentName(strings.TrimSpace(name)); err != nil {
			return cfg, err
		}
		cfg.Name = strings.TrimSpace(name)
	}

	// Type (provider or consumer). Skip prompt if --type flag was passed.
	if typeFromFlag != "" {
		cfg.Type = typeFromFlag
		displayType := "Provider"
		if typeFromFlag == "consumer" {
			displayType = "Consumer"
		}
		fmt.Printf("+ Type: %s\n", displayType)
	} else {
		typeIdx, err := InteractiveSelect("Type", []string{"Provider", "Consumer"}, 0)
		if err != nil {
			return cfg, err
		}
		cfg.Type = typeFromIndex(typeIdx)
	}

	// DisplayName and Description are provider-only; consumers have no
	// agent identity and don't publish an agent-card.
	if cfg.Type == "provider" {
		displayName, err := readLine(r, "Display name", cfg.Name)
		if err != nil {
			return cfg, err
		}
		cfg.DisplayName = displayName

		desc, err := readLine(r, "Description", cfg.Name+" agent")
		if err != nil {
			return cfg, err
		}
		cfg.Description = desc
	} else {
		// Consumer: fill a sensible default for pyproject.toml metadata.
		cfg.Description = cfg.Name + " consumer"
	}

	// Language (both provider and consumer need it)
	if langFromFlag != "" {
		cfg.Language = langFromFlag
		lang := "Python"
		if langFromFlag == "node" {
			lang = "Node"
		}
		fmt.Printf("+ Language: %s\n", lang)
	} else {
		lang, err := InteractiveSelect("Language", []string{"Python", "Node"}, 0)
		if err != nil {
			return cfg, err
		}
		if lang == 0 {
			cfg.Language = "python"
		} else {
			cfg.Language = "node"
		}
	}

	if cfg.Type == "consumer" {
		// Consumer scaffolds don't use concurrency/instances/streaming/task-kinds/docker.
		return cfg, nil
	}

	// Concurrency
	conc, err := readInt(r, "Max concurrent tasks", 1, 1)
	if err != nil {
		return cfg, err
	}
	cfg.Concurrency = conc

	// Expected instances
	inst, err := readInt(r, "Expected instances", 1, 1)
	if err != nil {
		return cfg, err
	}
	cfg.ExpectedInstances = inst

	// Streaming
	streaming, err := readConfirm(r, "Enable streaming?", false)
	if err != nil {
		return cfg, err
	}
	cfg.Streaming = streaming

	// Task kind
	taskKindIdx, err := InteractiveSelect("Task kind", []string{"request", "pipe", "both"}, 0)
	if err != nil {
		return cfg, err
	}
	switch taskKindIdx {
	case 0:
		cfg.TaskKinds = []string{"request"}
	case 1:
		cfg.TaskKinds = []string{"pipe"}
	case 2:
		cfg.TaskKinds = []string{"request", "pipe"}
	}

	// Docker
	docker, err := readConfirm(r, "Add Docker support?", false)
	if err != nil {
		return cfg, err
	}
	cfg.Docker = docker

	return cfg, nil
}

// typeFromIndex maps the InteractiveSelect index for the Type prompt to
// the canonical Config.Type string. Kept as a free function so tests can
// exercise it without driving stdin.
func typeFromIndex(idx int) string {
	if idx == 1 {
		return "consumer"
	}
	return "provider"
}

func readLine(r *bufio.Reader, prompt string, defaultVal string) (string, error) {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return defaultVal, nil
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

func readInt(r *bufio.Reader, prompt string, defaultVal int, min int) (int, error) {
	for {
		fmt.Printf("%s [%d]: ", prompt, defaultVal)
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return defaultVal, nil
			}
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultVal, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < min {
			fmt.Printf("  Must be a number >= %d\n", min)
			continue
		}
		return n, nil
	}
}

func readConfirm(r *bufio.Reader, prompt string, defaultYes bool) (bool, error) {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}
	fmt.Printf("%s [%s]: ", prompt, hint)
	line, err := r.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return defaultYes, nil
		}
		return false, err
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes, nil
	}
	return line == "y" || line == "yes", nil
}
