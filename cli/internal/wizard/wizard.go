package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var agentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// projectNameRe constrains a webapp project directory name to safe characters
// (no path separators, no spaces). It is intentionally looser than
// agentNameRe — a directory may contain '.' and '-'.
var projectNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// maxAgentsPerWebapp caps how many agents a single webapp page may wire up.
// Mirrors the limit enforced by internal/config.Validate and cmd/init.go.
const maxAgentsPerWebapp = 25

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

	helpWebappName = "  A name for your web app project. This becomes the directory the files are\n" +
		"  scaffolded into. Letters, numbers, dots, dashes, and underscores only."

	helpWebappAgents = "  The bare name(s) of the Blocks agent(s) this page will call (e.g. 'translator').\n" +
		"  Start typing to search the registry — public agents plus any private ones\n" +
		"  your account can access. You can add several agents to one page."

	helpProjectKind = "  Agent: build an agent that processes tasks, or a consumer that calls agents.\n" +
		"  Web app: scaffold a static page pre-wired with the Blocks embed-auth widget\n" +
		"  that calls one or more existing agents."
)

// Config holds all wizard answers needed to scaffold a project.
type Config struct {
	Name              string   // machine identifier (agentName)
	DisplayName       string   // human-readable display name
	Description       string
	Language          string   // "node" or "python"
	Mode              string   // "provider" (default) | "consumer" | "webapp"
	Concurrency       int
	ExpectedInstances int
	Streaming         bool
	TaskKinds         []string // "request", "pipe", or both
	Docker            bool

	// Webapp-scaffold fields (only used when Mode == "webapp").
	Agents        []string // one or more bare agent names (each ^[a-zA-Z0-9_]+$)
	BlocksBaseURL string   // defaults to "https://blocks.ai" when empty
}

// ValidateAgentName checks that name matches /^[a-zA-Z0-9_]+$/.
func ValidateAgentName(name string) error {
	if !agentNameRe.MatchString(name) {
		return fmt.Errorf("agentName must contain only alphanumeric characters and underscores")
	}
	return nil
}

// ValidateProjectName checks that name is a safe directory name.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("name cannot be %q", name)
	}
	if !projectNameRe.MatchString(name) {
		return fmt.Errorf("use only letters, numbers, '.', '-', and '_' (no spaces or slashes)")
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
		Mode:              "provider",
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
// If modeFromFlag is non-empty ("provider" or "consumer"), the mode prompt is skipped.
func Run(nameFromArgs string, langFromFlag string, modeFromFlag string) (Config, error) {
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

	// Mode (provider or consumer). Skip prompt if --mode flag was passed.
	if modeFromFlag != "" {
		cfg.Mode = modeFromFlag
		displayMode := "Provider"
		if modeFromFlag == "consumer" {
			displayMode = "Consumer"
		}
		fmt.Printf("+ Mode: %s\n", displayMode)
	} else {
		modeIdx, err := InteractiveSelect("Mode", []string{"Provider", "Consumer"}, 0, helpType)
		if err != nil {
			return cfg, err
		}
		cfg.Mode = modeFromIndex(modeIdx)
	}

	// DisplayName and Description are provider-only; consumers have no
	// agent identity and don't publish an agent-card.
	if cfg.Mode == "provider" {
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

	if cfg.Mode == "consumer" {
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

// Project-kind indices returned by SelectProjectKind.
const (
	ProjectKindAgent  = 0
	ProjectKindWebapp = 1
)

// SelectProjectKind asks the top-level "Agent vs Web app" question and returns
// ProjectKindAgent or ProjectKindWebapp. On a non-terminal stdin it returns
// the default (ProjectKindAgent), preserving the historical agent behavior.
func SelectProjectKind() (int, error) {
	return InteractiveSelect("What are you building?", []string{"Agent", "Web app"}, ProjectKindAgent, helpProjectKind)
}

// RunWebapp executes the interactive webapp wizard: it prompts for a project
// name, then collects one or more agent names via the live type-ahead
// autocomplete (backed by suggest). It returns a Config with Mode == "webapp".
//
// The project-name prompt runs in canonical line mode; the agent-collection
// loop runs under a single raw-mode session so only one goroutine ever reads
// stdin. When stdin is not a terminal, the agent loop falls back to plain line
// prompts (no live suggestions).
func RunWebapp(ctx context.Context, suggest SuggestFunc) (Config, error) {
	r := bufio.NewReader(os.Stdin)

	var name string
	for {
		n, err := readLine(r, "Web app name", "", helpWebappName)
		if err != nil {
			return Config{}, err
		}
		n = strings.TrimSpace(n)
		if err := ValidateProjectName(n); err != nil {
			fmt.Printf("  Invalid: %v\n", err)
			continue
		}
		name = n
		break
	}

	ri, ok := newRawInput()
	if !ok {
		agents, err := collectAgentsPlain(r)
		if err != nil {
			return Config{}, err
		}
		return Config{Mode: "webapp", Name: name, Agents: agents}, nil
	}
	defer ri.close()

	fmt.Print("\r\nAdd the agents this web app will call (esc when done):\r\n")
	var agents []string
	for {
		value, err := ri.autocomplete(ctx, "Agent to use", suggest, ValidateAgentName)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				if len(agents) > 0 {
					break // esc finishes once we have at least one agent
				}
				return Config{}, fmt.Errorf("canceled")
			}
			return Config{}, err
		}

		switch {
		case containsString(agents, value):
			fmt.Printf("  %s already added.\r\n", value)
		case len(agents) >= maxAgentsPerWebapp:
			fmt.Printf("  Reached the %d-agent limit.\r\n", maxAgentsPerWebapp)
		default:
			agents = append(agents, value)
		}
		if len(agents) >= maxAgentsPerWebapp {
			break
		}

		more, err := ri.confirm("Add another agent?", false)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				break
			}
			return Config{}, err
		}
		if !more {
			break
		}
	}

	if len(agents) == 0 {
		return Config{}, fmt.Errorf("at least one agent is required")
	}
	return Config{Mode: "webapp", Name: name, Agents: agents}, nil
}

// collectAgentsPlain is the non-TTY fallback for agent collection: repeated
// line prompts with no live suggestions.
func collectAgentsPlain(r *bufio.Reader) ([]string, error) {
	var agents []string
	for {
		line, err := readLine(r, "Agent name (blank to finish)", "", helpWebappAgents)
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if len(agents) == 0 {
				fmt.Println("  At least one agent is required.")
				continue
			}
			return agents, nil
		}
		if err := ValidateAgentName(line); err != nil {
			fmt.Printf("  Invalid: %v\n", err)
			continue
		}
		if containsString(agents, line) {
			fmt.Printf("  %s already added.\n", line)
			continue
		}
		if len(agents) >= maxAgentsPerWebapp {
			fmt.Printf("  Reached the %d-agent limit.\n", maxAgentsPerWebapp)
			return agents, nil
		}
		agents = append(agents, line)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// modeFromIndex maps the InteractiveSelect index for the Mode prompt to
// the canonical Config.Mode string. Kept as a free function so tests can
// exercise it without driving stdin.
func modeFromIndex(idx int) string {
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
