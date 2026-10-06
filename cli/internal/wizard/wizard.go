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

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
)

var agentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// agentNameFlagHint and webappNameFlagHint name the argument that supplies each
// project name without a terminal. They are the answer to "how do I run this in
// a script?", so they are phrased as the command a caller can copy.
const (
	agentNameFlagHint  = "the name as the first argument (blocks init <name>)"
	webappNameFlagHint = "the name as the first argument (blocks init <name> --mode webapp --agent <agent>)"
	callerNameFlagHint = "the name as the first argument (blocks init <name> --mode consumer --yes)"
)

// helpAgentNameText is the agent-name prompt help. Like helpDisplayNameText it
// reads the active product name and deployment kind at call time so an
// enterprise deployment describes reachability on that deployment rather than a
// marketplace of consumers.
func helpAgentNameText() string {
	text := "  A unique identifier for your agent on " + branding.ProductName() + ".\n" +
		"  Must contain only letters, numbers, and underscores (e.g. my_weather_agent).\n" +
		"  This becomes the agentName in your agent-card.json and is how other agents\n"
	if clictx.Enterprise() {
		return text + "  reach yours on this deployment."
	}
	return text + "  and consumers find yours."
}

// helpDisplayNameText is the display-name prompt help. It reads the active
// product name at call time so enterprise deployments brand it correctly.
func helpDisplayNameText() string {
	return "  A human-readable name shown in the " + branding.ProductName() + " UI (e.g. \"Weather Forecast Agent\").\n" +
		"  Defaults to your agent name. Can include spaces and special characters."
}

// projectNameRe constrains a webapp project directory name to safe characters
// (no path separators, no spaces). It is intentionally looser than
// agentNameRe — a directory may contain '.' and '-'.
var projectNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// callerProjectNameRe is stricter than projectNameRe because a caller project
// name becomes the pyproject.toml / package.json package name, which must start
// and end with a letter or digit.
var callerProjectNameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`)

// minExpectedInstances is the agent-card schema minimum (TestExpectedInstancesMinimumMatchesAgentCardSchema).
const minExpectedInstances = 0

// maxAgentsPerWebapp caps how many agents a single webapp page may wire up.
// Mirrors the limit enforced by internal/config.Validate and cmd/init.go.
const maxAgentsPerWebapp = 25

// AgentValidateFunc validates a value accepted by the webapp wizard's
// agent autocomplete. fromSuggestion is true when the value was picked from
// the registry's suggestion list — the registry already vouched that the
// agent exists, so an existence check would be a redundant round-trip.
type AgentValidateFunc func(value string, fromSuggestion bool) error

// Help text constants for inline ? help across wizard prompts.
const (
	helpDescription = "  A short sentence describing what your agent does. Shown on the Discover page\n" +
		"  and in agent cards. Helps others understand when to use your agent."

	helpLanguage = "  The programming language for your handler code.\n" +
		"  Python: Uses blocks_network SDK, pip, and a Python handler.\n" +
		"  Node: Uses @blocks-network/sdk, npm, and a TypeScript handler.\n" +
		"  Both have full feature parity."

	helpCallerLanguage = "  The programming language for your calling script.\n" +
		"  Python: main.py calls agents with TaskClient from the blocks_network SDK (pip).\n" +
		"  Node: index.ts calls agents with TaskClient from @blocks-network/sdk (npm).\n" +
		"  Both have full feature parity."

	helpCallerName = "  A name for your project. This becomes the directory the calling script is\n" +
		"  scaffolded into. Letters, numbers, dots, dashes, and underscores only."

	helpConcurrency = "  How many tasks your agent can process simultaneously per instance.\n" +
		"  Set to 1 for sequential processing (simplest)."

	helpInstances = "  How many copies of your agent you plan to run. Set to 1 if running on a\n" +
		"  single machine. Higher values tell the network to distribute tasks across\n" +
		"  multiple instances for load balancing. 0 is broadcast: every running instance\n" +
		"  receives every task, so use it only if duplicate processing is fine."

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

	helpAddAnotherAgent = "  Yes searches for another agent to add to this page. No, or Enter, finishes\n" +
		"  the list; you can still remove agents in the next step."

	helpRemoveAgent = "  Yes lets you pick an added agent to drop from this page. No, or Enter, keeps\n" +
		"  the list as it is."

	helpAgentToRemove = "  Type to filter the agents you added, then press Enter to drop the highlighted\n" +
		"  one from this page. Esc keeps the list as it is."

	helpWebappName = "  A name for your web app project. This becomes the directory the files are\n" +
		"  scaffolded into. Letters, numbers, dots, dashes, and underscores only."
)

// HelpWebappAgentsText is the agent-collection prompt help. It reads the
// active product name at call time so an enterprise deployment names itself
// rather than the public network.
func HelpWebappAgentsText() string {
	return "  The bare name(s) of the " + branding.ProductName() + " agent(s) this page will call (e.g. 'translator').\n" +
		"  Start typing to search the registry — public agents plus any private ones\n" +
		"  your account can access. You can add several agents to one page."
}

func HelpAgentToCallText() string {
	return "  The bare name of the " + branding.ProductName() + " agent your script will call (e.g. 'translator').\n" +
		"  Start typing to search the registry — public agents plus any private ones\n" +
		"  your account can access. Esc skips this; the script then tells you how to\n" +
		"  pick an agent before it sends anything."
}

// HelpProjectKindText is the project-kind prompt help. It reads the active
// product name and deployment kind at call time so an enterprise deployment
// describes reachability on that deployment rather than a marketplace.
func HelpProjectKindText() string {
	reach := "so others on " + branding.ProductName() + " can call it."
	if clictx.Enterprise() {
		reach = "so other agents and apps on this deployment can call it."
	}
	return "  Call an agent: a short script that sends a task to an existing agent and\n" +
		"  prints the result. You don't need an agent of your own.\n" +
		"  Build an agent: a handler that processes tasks, registered " + reach + "\n" +
		"  Build a web app: a static page pre-wired with the " + branding.ProductName() + " embed-auth widget\n" +
		"  that calls one or more existing agents."
}

// Config holds all wizard answers needed to scaffold a project.
type Config struct {
	Name              string // machine identifier (agentName)
	DisplayName       string // human-readable display name
	Description       string
	Language          string // "node" or "python"
	Mode              string // "provider" (default) | "consumer" | "webapp"
	Concurrency       int
	ExpectedInstances int
	Streaming         bool
	TaskKinds         []string // "request", "pipe", or both
	Docker            bool

	// TargetAgent is the agent a consumer script calls; empty leaves the script's placeholder.
	TargetAgent string
	// TargetBillingMode is TargetAgent's registered billingMode; empty when it could not be looked up.
	TargetBillingMode string

	// Webapp-scaffold fields (only used when Mode == "webapp").
	Agents         []string // one or more bare agent names (each ^[a-zA-Z0-9_]+$)
	BlocksBaseURL  string   // defaults to "https://app.blocks.ai" when empty
	BackendBaseURL string   // backend API origin baked into web/app.js; empty → resolved to the asset base URL at scaffold time
}

// CallerBillingMode is the billingMode a consumer script passes to TaskClient.create,
// which the backend rejects unless it matches the agent's. An unknown mode renders "free".
func (c Config) CallerBillingMode() string {
	if c.TargetBillingMode == "paid" {
		return "paid"
	}
	return "free"
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

// ValidateCallerProjectName checks that name is usable as both a directory and a package name.
func ValidateCallerProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !callerProjectNameRe.MatchString(name) {
		return fmt.Errorf("use only letters, numbers, '.', '-', and '_', starting and ending with a letter or number (no spaces or slashes)")
	}
	return nil
}

// modeAliases maps every accepted --mode spelling to its canonical value.
// The canonical values are "provider" and "consumer": they select the template
// directory, are validated by internal/scaffold, and are written into
// scaffolded projects. The connect-agent / call-agent spellings exist because
// "provider" and "consumer" are marketplace terms that mean nothing inside an
// enterprise deployment. Aliases are accepted on every profile so a CI script
// is never sensitive to which profile is active.
var modeAliases = map[string]string{
	"provider":      "provider",
	"consumer":      "consumer",
	"connect-agent": "provider",
	"call-agent":    "consumer",
}

// NormalizeMode resolves any accepted --mode spelling to its canonical value.
// ok is false for unrecognized input, including "webapp" (a project kind, not
// a mode) and the empty string.
func NormalizeMode(raw string) (string, bool) {
	canonical, ok := modeAliases[strings.ToLower(strings.TrimSpace(raw))]
	return canonical, ok
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

func DefaultCallerConfig(name string) Config {
	return Config{
		Name:        name,
		Description: "A script that calls agents",
		Language:    "python",
		Mode:        "consumer",
	}
}

// Run executes the interactive wizard for an agent project; Caller projects use RunCaller.
// If nameFromArgs is non-empty, the name prompt is skipped.
// If langFromFlag is non-empty ("node" or "python"), the language prompt is skipped.
func Run(nameFromArgs string, langFromFlag string) (Config, error) {
	r := bufio.NewReader(os.Stdin)
	cfg := Config{Mode: "provider"}

	if nameFromArgs != "" {
		if err := ValidateAgentName(nameFromArgs); err != nil {
			return cfg, err
		}
		cfg.Name = nameFromArgs
	} else {
		name, err := readRequiredLine(r, "Agent name (letters, numbers, underscores)", agentNameFlagHint, helpAgentNameText(), ValidateAgentName)
		if err != nil {
			return cfg, err
		}
		cfg.Name = name
	}

	displayName, err := readLine(r, "Display name", cfg.Name, helpDisplayNameText())
	if err != nil {
		return cfg, err
	}
	cfg.DisplayName = displayName

	desc, err := readLine(r, "Description", cfg.Name+" agent", helpDescription)
	if err != nil {
		return cfg, err
	}
	cfg.Description = desc

	if cfg.Language, err = selectLanguage(langFromFlag, helpLanguage); err != nil {
		return cfg, err
	}

	// Concurrency
	conc, err := readInt(r, "Max concurrent tasks", 1, 1, helpConcurrency)
	if err != nil {
		return cfg, err
	}
	cfg.Concurrency = conc

	// Expected instances
	inst, err := readInt(r, "Expected instances", 1, minExpectedInstances, helpInstances)
	if err != nil {
		return cfg, err
	}
	cfg.ExpectedInstances = inst

	// Streaming
	streaming, err := Confirm(r, "Enable streaming?", false, helpStreaming)
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
	docker, err := Confirm(r, "Add Docker support?", false, helpDocker)
	if err != nil {
		return cfg, err
	}
	cfg.Docker = docker

	return cfg, nil
}

func selectLanguage(langFromFlag, helpText string) (string, error) {
	if langFromFlag != "" {
		display := "Python"
		if langFromFlag == "node" {
			display = "Node"
		}
		fmt.Printf("+ Language: %s\n", display)
		return langFromFlag, nil
	}
	idx, err := InteractiveSelect("Language", []string{"Python", "Node"}, 0, helpText)
	if err != nil {
		return "", err
	}
	if idx == 1 {
		return "node", nil
	}
	return "python", nil
}

const (
	ProjectKindCaller = iota
	ProjectKindAgent
	ProjectKindWebapp
)

func projectKindLabels() []string {
	call := "Call an agent on the network"
	if clictx.Enterprise() {
		call = "Call an agent on Blocks Enterprise"
	}
	return []string{call, "Build an agent that others can call", "Build a web app that calls agents"}
}

func SelectProjectKind() (int, error) {
	return InteractiveSelect("What do you want to do?", projectKindLabels(), ProjectKindCaller, HelpProjectKindText())
}

type CallerOptions struct {
	Name     string
	Language string
	Agent    string
	Suggest  SuggestFunc
	Validate AgentValidateFunc
}

// RunCaller asks for the agent last: its raw-mode reader leaves a goroutine parked on stdin, so no line prompt may follow.
func RunCaller(ctx context.Context, opts CallerOptions) (Config, error) {
	r := bufio.NewReader(os.Stdin)

	name := opts.Name
	var err error
	if name != "" {
		err = ValidateCallerProjectName(name)
	} else {
		name, err = readRequiredLine(r, "Project name", callerNameFlagHint, helpCallerName, ValidateCallerProjectName)
	}
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultCallerConfig(name)

	if cfg.Language, err = selectLanguage(opts.Language, helpCallerLanguage); err != nil {
		return cfg, err
	}

	if opts.Agent != "" {
		fmt.Printf("+ Agent to call: %s\n", opts.Agent)
		cfg.TargetAgent = opts.Agent
		return cfg, nil
	}
	cfg.TargetAgent, err = pickAgentToCall(ctx, r, opts.Suggest, opts.Validate)
	return cfg, err
}

func pickAgentToCall(ctx context.Context, r *bufio.Reader, suggest SuggestFunc, validate AgentValidateFunc) (string, error) {
	ri, ok := newRawInput()
	if !ok {
		for {
			line, err := readLine(r, "Agent to call (blank to skip)", "", HelpAgentToCallText())
			if err != nil {
				return "", err
			}
			line = strings.TrimSpace(line)
			if line == "" {
				return "", nil
			}
			if err := ValidateAgentName(line); err != nil {
				fmt.Printf("  Invalid: %v\n", err)
				continue
			}
			return line, nil
		}
	}
	defer ri.close()

	fmt.Print("\r\nSearch for the agent this script will call (esc to skip):\r\n")
	value, err := ri.autocomplete(ctx, "Agent to call", HelpAgentToCallText(), suggest, validate, AgentNoMatchHint)
	if errors.Is(err, ErrCanceled) {
		fmt.Print("  Skipped; the script explains how to pick an agent.\r\n")
		return "", nil
	}
	return value, err
}

// RunWebapp executes the interactive webapp wizard: it prompts for a project
// name, then collects one or more agent names via the live type-ahead
// autocomplete (backed by suggest, with Enter-time validation via validate).
// It returns a Config with Mode == "webapp".
//
// The project-name prompt runs in canonical line mode; the agent-collection
// loop runs under a single raw-mode session so only one goroutine ever reads
// stdin. When stdin is not a terminal, the agent loop falls back to plain line
// prompts (no live suggestions, regex-only validation).
//
// The name prompt is unconditional and gated, so under --no-input this function
// refuses before the raw-mode reader is ever started — the agent loop's own
// keypress reads need no separate gate.
func RunWebapp(ctx context.Context, nameFromArgs string, suggest SuggestFunc, validate AgentValidateFunc) (Config, error) {
	r := bufio.NewReader(os.Stdin)

	name := nameFromArgs
	var err error
	if name != "" {
		err = ValidateProjectName(name)
	} else {
		name, err = readRequiredLine(r, "Web app name", webappNameFlagHint, helpWebappName, ValidateProjectName)
	}
	if err != nil {
		return Config{}, err
	}

	ri, ok := newRawInput()
	if !ok {
		agents, err := collectAndReviewAgentsPlain(r)
		if err != nil {
			return Config{}, err
		}
		return Config{Mode: "webapp", Name: name, Agents: agents}, nil
	}
	defer ri.close()

	// Collect, review, and — if the review removed everything — collect
	// again. A wrong pick used to cost a Ctrl+C and a full restart; now it
	// costs one removal, and even removing every agent only loops back to
	// the collection prompt.
	var agents []string
	for {
		fmt.Print("\r\nAdd the agents this web app will call (esc when done):\r\n")
		agents, err = collectAgentsTTY(ri, ctx, suggest, validate)
		if err != nil {
			return Config{}, err
		}
		agents, err = reviewAgentsTTY(ri, ctx, agents)
		if err != nil {
			return Config{}, err
		}
		if len(agents) > 0 {
			break
		}
		fmt.Print("\r\nAll agents removed — add at least one.\r\n")
	}
	return Config{Mode: "webapp", Name: name, Agents: agents}, nil
}

func AgentNoMatchHint(query string) string {
	return fmt.Sprintf("No agents match %q. Try fewer or broader words, or run 'blocks search' to browse the catalog.", query)
}

// collectAgentsTTY is the interactive agent-collection loop: type-ahead
// autocomplete per agent, then an add-another confirm, until Esc, a "no",
// or the per-page limit.
func collectAgentsTTY(ri *rawInput, ctx context.Context, suggest SuggestFunc, validate AgentValidateFunc) ([]string, error) {
	var agents []string
	for {
		value, err := ri.autocomplete(ctx, "Agent to use", HelpWebappAgentsText(), suggest, validate, AgentNoMatchHint)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				if len(agents) > 0 {
					break // esc finishes once we have at least one agent
				}
				return nil, fmt.Errorf("canceled")
			}
			return nil, err
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

		more, err := ri.confirm("Add another agent?", false, helpAddAnotherAgent)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				break
			}
			return nil, err
		}
		if !more {
			break
		}
	}

	if len(agents) == 0 {
		return nil, fmt.Errorf("at least one agent is required")
	}
	return agents, nil
}

// reviewAgentsTTY shows the collected list and offers removal before
// anything is scaffolded. Esc at the confirmation finishes the review; Esc
// at the pick is "never mind" and returns to the confirmation. An empty
// result is legal here — the caller loops back to collection.
func reviewAgentsTTY(ri *rawInput, ctx context.Context, agents []string) ([]string, error) {
	printList := func() {
		fmt.Printf("\r\n  Agents (%d): %s\r\n", len(agents), strings.Join(agents, ", "))
	}
	printList()
	for len(agents) > 0 {
		remove, err := ri.confirm("Remove an agent?", false, helpRemoveAgent)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				return agents, nil
			}
			return nil, err
		}
		if !remove {
			return agents, nil
		}

		// The pick reuses the autocomplete: suggestions are the collected
		// names filtered by the query, and Enter on anything not in the
		// list is rejected inline.
		value, err := ri.autocomplete(ctx, "Agent to remove", helpAgentToRemove,
			func(_ context.Context, q string) ([]Suggestion, error) {
				var out []Suggestion
				for _, a := range agents {
					if strings.Contains(strings.ToLower(a), strings.ToLower(q)) {
						out = append(out, Suggestion{Value: a})
					}
				}
				return out, nil
			},
			func(value string, _ bool) error {
				if !containsString(agents, value) {
					return fmt.Errorf("%q is not in the list above", value)
				}
				return nil
			}, nil)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				continue
			}
			return nil, err
		}
		agents = removeString(agents, value)
		printList()
	}
	return agents, nil
}

// collectAndReviewAgentsPlain is the non-TTY fallback: line-based collection
// followed by the same review, restarting collection if the review removed
// everything.
func collectAndReviewAgentsPlain(r *bufio.Reader) ([]string, error) {
	for {
		agents, err := collectAgentsPlain(r)
		if err != nil {
			return nil, err
		}
		agents, err = reviewAgentsPlain(r, agents)
		if err != nil {
			return nil, err
		}
		if len(agents) > 0 {
			return agents, nil
		}
		fmt.Println("  All agents removed — at least one is required.")
	}
}

// reviewAgentsPlain is the line-based review: print the list, read a name to
// remove, blank line finishes.
func reviewAgentsPlain(r *bufio.Reader, agents []string) ([]string, error) {
	for len(agents) > 0 {
		fmt.Printf("  Agents (%d): %s\n", len(agents), strings.Join(agents, ", "))
		line, err := readLine(r, "Agent to remove (blank to finish review)", "", HelpWebappAgentsText())
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return agents, nil
		}
		if !containsString(agents, line) {
			fmt.Printf("  %q is not in the list.\n", line)
			continue
		}
		agents = removeString(agents, line)
	}
	return agents, nil
}

// collectAgentsPlain is the non-TTY fallback for agent collection: repeated
// line prompts with no live suggestions.
func collectAgentsPlain(r *bufio.Reader) ([]string, error) {
	var agents []string
	for {
		line, err := readLine(r, "Agent name (blank to finish)", "", HelpWebappAgentsText())
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

// removeString returns xs without want. Agent names are unique within a
// webapp list, so at most one entry can match.
func removeString(xs []string, want string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != want {
			out = append(out, x)
		}
	}
	return out
}

func readLine(r *bufio.Reader, prompt string, defaultVal string, helpText string) (string, error) {
	if noInputMode {
		return "", fmt.Errorf("cannot ask %q with --no-input", prompt)
	}
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
		if IsHelpRequest(line) {
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
	if noInputMode {
		return 0, fmt.Errorf("cannot ask %q with --no-input", prompt)
	}
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
		if IsHelpRequest(line) {
			fmt.Println(helpText)
			fmt.Println()
			continue
		}
		if line == "" {
			return defaultVal, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < min {
			fmt.Printf("  Enter a whole number of %d or more.\n", min)
			continue
		}
		return n, nil
	}
}

func Confirm(r *bufio.Reader, prompt string, defaultYes bool, helpText string) (bool, error) {
	if noInputMode {
		return false, fmt.Errorf("cannot ask %q with --no-input", prompt)
	}
	var readErr error
	answer, answered := AskYesNo(func() (string, bool) {
		line, err := r.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			return "", false
		}
		return line, true
	}, prompt, defaultYes, helpText)
	switch {
	case readErr != nil:
		return false, readErr
	case !answered:
		return defaultYes, nil
	}
	return answer, nil
}

// AskYesNo is the one y/n loop every prompt shares; answered is false when read reports end of input.
func AskYesNo(read func() (line string, ok bool), prompt string, defaultYes bool, helpText string) (answer, answered bool) {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}
	for {
		fmt.Printf("%s [%s] (? for help): ", prompt, hint)
		line, ok := read()
		if !ok {
			return false, false
		}
		line = strings.TrimSpace(strings.ToLower(line))
		switch {
		case IsHelpRequest(line):
			fmt.Println(helpText)
			fmt.Println()
		case line == "":
			return defaultYes, true
		case line == "y" || line == "yes":
			return true, true
		case line == "n" || line == "no":
			return false, true
		default:
			fmt.Println("  Please answer y or n.")
		}
	}
}
