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

// Help text constants for inline ? help across wizard prompts.
const (
	helpType = "  Provider: You're building an agent that processes tasks.\n" +
		"  Consumer: You're building a client that calls other agents.\n" +
		"  Choose Provider if you're unsure."

	helpDisplayName = "  A human-readable name shown in the Blocks Network UI (e.g. \"Weather Forecast Agent\").\n" +
		"  Defaults to your agent name. Can include spaces and special characters."

	helpDescription = "  A short sentence describing what your agent does. Shown on the Discover page\n" +
		"  and in agent cards. Helps others understand when to use your agent."

	helpLanguage = "  The programming language for your handler code.\n" +
		"  Python: Uses blocks_network SDK, pip, and a Python handler.\n" +
		"  Node: Uses @blocks-network/sdk, npm, and a TypeScript handler.\n" +
		"  Both have full feature parity."

	helpConcurrency = "  How many tasks your agent can process simultaneously per instance.\n" +
		"  Set to 1 for sequential processing (simplest)."

	helpInstances = "  How many copies of your agent you plan to run. Set to 1 if running on a\n" +
		"  single machine. Higher values tell the network to distribute tasks across\n" +
		"  multiple instances for load balancing."

	helpStreaming = "  Adds real-time streaming support so your agent can send incremental results\n" +
		"  to callers as it works. If your agent just returns a final result, you don't\n" +
		"  need this. You can add it later."

	helpTaskKind = "  Request: One-shot tasks. Caller sends input, agent returns a result (most common).\n" +
		"  Pipe: Long-running sessions with a set duration. Useful for continuous monitoring\n" +
		"  or live transcription.\n" +
		"  Both: Agent handles both types. Choose Request if you're unsure."

	helpDocker = "  Adds a Dockerfile to your project for deploying your agent as a container.\n" +
		"  If you're just running locally with 'blocks run', you don't need this.\n" +
		"  You can add it later."
)

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
		for {
			fmt.Print("Agent name (letters, numbers, underscores, ? for help): ")
			line, err := r.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return cfg, fmt.Errorf("name is required")
				}
				return cfg, err
			}
			name := strings.TrimSpace(line)
			if name == "?" {
				fmt.Println("  A unique identifier for your agent on the Blocks Network.")
				fmt.Println("  Must contain only letters, numbers, and underscores (e.g. my_weather_agent).")
				fmt.Println("  This becomes the agentName in your agent-card.json and is how other agents")
				fmt.Println("  and consumers find yours.")
				fmt.Println()
				continue
			}
			if name == "" {
				fmt.Println("  Name is required.")
				continue
			}
			if err := ValidateAgentName(name); err != nil {
				fmt.Printf("  Invalid: %v\n", err)
				continue
			}
			cfg.Name = name
			break
		}
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
		typeIdx, err := InteractiveSelect("Type", []string{"Provider", "Consumer"}, 0, helpType)
		if err != nil {
			return cfg, err
		}
		cfg.Type = typeFromIndex(typeIdx)
	}

	// DisplayName and Description are provider-only; consumers have no
	// agent identity and don't publish an agent-card.
	if cfg.Type == "provider" {
		displayName, err := readLine(r, "Display name", cfg.Name, helpDisplayName)
		if err != nil {
			return cfg, err
		}
		cfg.DisplayName = displayName

		desc, err := readLine(r, "Description", cfg.Name+" agent", helpDescription)
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
		lang, err := InteractiveSelect("Language", []string{"Python", "Node"}, 0, helpLanguage)
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
	conc, err := readInt(r, "Max concurrent tasks", 1, 1, helpConcurrency)
	if err != nil {
		return cfg, err
	}
	cfg.Concurrency = conc

	// Expected instances
	inst, err := readInt(r, "Expected instances", 1, 1, helpInstances)
	if err != nil {
		return cfg, err
	}
	cfg.ExpectedInstances = inst

	// Streaming
	streaming, err := readConfirm(r, "Enable streaming?", false, helpStreaming)
	if err != nil {
		return cfg, err
	}
	cfg.Streaming = streaming

	// Task kind
	taskKindIdx, err := InteractiveSelect("Task kind", []string{"request", "pipe", "both"}, 0, helpTaskKind)
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
	docker, err := readConfirm(r, "Add Docker support?", false, helpDocker)
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

func readLine(r *bufio.Reader, prompt string, defaultVal string, helpText string) (string, error) {
	for {
		if defaultVal != "" {
			fmt.Printf("%s [%s] (? for help): ", prompt, defaultVal)
		} else {
			fmt.Printf("%s (? for help): ", prompt)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return defaultVal, nil
			}
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "?" {
			fmt.Println(helpText)
			fmt.Println()
			continue
		}
		if line == "" {
			return defaultVal, nil
		}
		return line, nil
	}
}

func readInt(r *bufio.Reader, prompt string, defaultVal int, min int, helpText string) (int, error) {
	for {
		fmt.Printf("%s [%d] (? for help): ", prompt, defaultVal)
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return defaultVal, nil
			}
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "?" {
			fmt.Println(helpText)
			fmt.Println()
			continue
		}
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

func readConfirm(r *bufio.Reader, prompt string, defaultYes bool, helpText string) (bool, error) {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}
	for {
		fmt.Printf("%s [%s] (? for help): ", prompt, hint)
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return defaultYes, nil
			}
			return false, err
		}
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "?" {
			fmt.Println(helpText)
			fmt.Println()
			continue
		}
		if line == "" {
			return defaultYes, nil
		}
		return line == "y" || line == "yes", nil
	}
}
