package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/pubnub/blocks-sdk/cli/internal/agentsearch"
	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	searchDefaultLimit        = 5
	searchTimeout             = 15 * time.Second
	pickerDisplayNameMaxCells = 48
	// pickerLimit matches the rows the init picker renders.
	pickerLimit = 10
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Find agents you can call",
	Long:  searchLongHelp(),
	Example: `  blocks search translator
  blocks search tag:sql --status all
  blocks search '"customer support"' --limit 10
  blocks search -- translator -legal
  blocks search weather --json`,
	RunE: runSearch,
}

func searchLongHelp() string {
	var b strings.Builder
	b.WriteString(`Search the registry for agents you can call, best match first. With no query,
lists agents alphabetically. Only online agents are listed unless you pass
--status. Logged in, results include your private and shared agents.

`)
	writeSearchSyntax(&b, searchStyle{})
	b.WriteString(`
In a shell, quote phrases and put -- before exclusions, as in the examples.

In a terminal, ← → page through results, ? shows the syntax, q quits.`)
	return b.String()
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().Int("limit", searchDefaultLimit, fmt.Sprintf("Maximum results (1-%d)", agentsearch.MaxLimit))
	searchCmd.Flags().Bool("json", false, "Output structured JSON")
	searchCmd.Flags().String("status", agentsearch.StatusOnline, "Agents to list by status: "+strings.Join(agentsearch.Statuses, ", "))
	_ = searchCmd.RegisterFlagCompletionFunc("status", cobra.FixedCompletions(agentsearch.Statuses, cobra.ShellCompDirectiveNoFileComp))
	searchCmd.Flags().String("cursor", "", "Start from a page cursor printed by a previous search (\"next\" in --json)")
}

func runSearch(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > agentsearch.MaxLimit {
		return fmt.Errorf("--limit must be between 1 and %d", agentsearch.MaxLimit)
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	cursor, _ := cmd.Flags().GetString("cursor")
	status, _ := cmd.Flags().GetString("status")
	if !slices.Contains(agentsearch.Statuses, status) {
		return fmt.Errorf("invalid --status %q — must be one of: %s", status, strings.Join(agentsearch.Statuses, ", "))
	}
	query := strings.TrimSpace(strings.Join(args, " "))

	client, err := searchClient()
	if err != nil {
		return err
	}
	interactive := !jsonOutput && !noInputMode && isInteractive() && term.IsTerminal(int(os.Stdout.Fd()))
	if interactive && !cmd.Flags().Changed("limit") {
		limit = pagerPageSize()
	}
	fetch := func(ctx context.Context, c string) (agentsearch.Result, error) {
		ctx, cancel := context.WithTimeout(ctx, searchTimeout)
		defer cancel()
		return agentsearch.Search(ctx, client, agentsearch.Query{Text: query, Limit: limit, Cursor: c, Status: status})
	}
	result, err := fetch(cmd.Context(), cursor)
	if err != nil {
		return fmt.Errorf("search failed: %s", searchFailureReason(err, client.BaseURL))
	}

	switch {
	case jsonOutput:
		return writeSearchJSON(os.Stdout, query, result)
	case interactive && result.Next != "" && len(result.Agents) > 0:
		return runSearchPager(cmd.Context(), query, result, func(ctx context.Context, c string) (agentsearch.Result, error) {
			r, err := fetch(ctx, c)
			if err != nil {
				return r, fmt.Errorf("search failed: %s", searchFailureReason(err, client.BaseURL))
			}
			return r, nil
		})
	}
	writeSearchResults(os.Stdout, stdoutSearchStyle(), query, result)
	next := nextPage{args: args, profile: rootProfile, status: status, cursor: result.Next}
	if cmd.Flags().Changed("limit") {
		next.limit = limit
	}
	writeSearchFooter(os.Stdout, result, next.command(shellQuoterFor(runtime.GOOS)))
	return nil
}

// nextPage is what the following page's command must repeat: the query, the deployment, an explicit page size and a non-default status.
type nextPage struct {
	args    []string
	profile string
	limit   int // 0 when --limit was not given
	status  string
	cursor  string
}

// command is how to print the following page: a shell command, or a --cursor hint when some word
// cannot be quoted for this shell. "" when there is no following page.
func (n nextPage) command(quote func(string) string) string {
	if n.cursor == "" {
		return ""
	}
	parts := []string{"blocks search"}
	add := func(words ...string) {
		parts = append(parts, words...)
	}
	if n.profile != "" {
		add("--profile", quote(n.profile))
	}
	if n.limit > 0 {
		add("--limit", fmt.Sprint(n.limit))
	}
	if n.status != "" && n.status != agentsearch.StatusOnline {
		add("--status", quote(n.status))
	}
	add("--cursor", quote(n.cursor))
	if len(n.args) > 0 {
		// Ends flag parsing, so a query word starting with "-" stays a query word.
		add("--")
		for _, a := range n.args {
			add(quote(a))
		}
	}
	if slices.Contains(parts, "") {
		return "repeat this search with --cursor " + quote(n.cursor)
	}
	return strings.Join(parts, " ")
}

func shellQuoterFor(goos string) func(string) string {
	if goos == "windows" {
		return windowsQuote
	}
	return posixQuote
}

func needsQuoting(s string) bool {
	return s == "" || strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.=/", r))
	}) >= 0
}

func posixQuote(s string) string {
	if !needsQuoting(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// windowsQuote quotes for both cmd.exe and PowerShell, or returns "" when no quoting survives both:
// PowerShell expands $ and ` and cmd.exe expands % inside double quotes, and neither escapes " the same way.
func windowsQuote(s string) string {
	if !needsQuoting(s) {
		return s
	}
	if strings.ContainsAny(s, "\"$`%") {
		return ""
	}
	// Backslashes before the closing quote are doubled so the quote still closes the argument.
	trailing := len(s) - len(strings.TrimRight(s, `\`))
	return `"` + s + strings.Repeat(`\`, trailing) + `"`
}

// An unusable credential errors instead of searching anonymously, which would silently hide private agents.
func searchClient() (*blocksapi.Client, error) {
	backend := trimURL(resolveBackendURL())
	if backend == "" {
		return nil, backendNotConfigured("BLOCKS_BACKEND_URL must be set (or configure via CDM)")
	}
	c := clictx.EffectiveCredential()
	switch {
	case c.Err != nil:
		return nil, c.Err
	case c.Key != "":
	case c.StoreErr != nil:
		return nil, fmt.Errorf("failed to load credentials: %w", c.StoreErr)
	case c.Expired:
		return nil, apiKeyExpiredError()
	}
	return blocksapi.NewClient(backend, c.Key), nil
}

func searchFailureReason(err error, backendURL string) string {
	switch agentsearch.KindOf(err) {
	case agentsearch.KindAuth:
		return "your credentials were rejected — run 'blocks login' to re-authenticate"
	case agentsearch.KindConnectivity:
		return fmt.Sprintf("cannot reach %s — check your network connection", termsafe.Text(originHost(backendURL)))
	default:
		return "the registry returned an error: " + termsafe.Message(err.Error())
	}
}

// statusFiltered reports whether result left out agents of some status.
func statusFiltered(result agentsearch.Result) bool {
	return result.Status != "" && result.Status != agentsearch.StatusAll
}

func searchEmptyMessage(query string, result agentsearch.Result) string {
	agents := "agents"
	if statusFiltered(result) {
		agents = result.Status + " agents"
	}
	var b strings.Builder
	if query == "" {
		fmt.Fprintf(&b, "No %s are visible to you yet.\n", agents)
	} else {
		fmt.Fprintf(&b, "No %s match %q.\n", agents, termsafe.Text(query))
		b.WriteString("  Try fewer or broader words, or run 'blocks search' with no query to browse the catalog.\n")
	}
	if statusFiltered(result) {
		fmt.Fprintf(&b, "%s%s\n", searchIndent, allStatusesHint)
	}
	if !result.Authenticated {
		b.WriteString("  Run 'blocks login' to include your private agents and agents shared with you.\n")
	}
	return b.String()
}

// searchStyle is how results are drawn: width in terminal cells, and whether ANSI color is allowed.
type searchStyle struct {
	width int
	color bool
}

const (
	defaultSearchWidth = 100
	searchIndent       = "  "
	// searchParseErrorNotice matches the dashboard's wording for the same registry flag.
	searchParseErrorNotice = "Some search operators couldn't be parsed — showing plain-text results."
	allStatusesHint        = "Add --status all to list agents of every status."
)

func stdoutSearchStyle() searchStyle {
	st := searchStyle{width: defaultSearchWidth}
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return st
	}
	if w, _, err := term.GetSize(fd); err == nil && w > 0 {
		st.width = w
	}
	st.color = os.Getenv("NO_COLOR") == ""
	return st
}

func (st searchStyle) paint(code, s string) string {
	if !st.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func writeSearchResults(w io.Writer, st searchStyle, query string, result agentsearch.Result) {
	if result.ParseError {
		fmt.Fprintf(w, "%s\n\n", st.paint("33", searchParseErrorNotice))
	}
	if len(result.Agents) == 0 {
		fmt.Fprint(w, searchEmptyMessage(query, result))
		return
	}
	lineWidth := max(st.width-len(searchIndent), 20)
	for i, a := range result.Agents {
		if i > 0 {
			fmt.Fprintln(w)
		}
		badges := st.badges(a)
		nameCells := max(st.width-2-runewidth.StringWidth(ansiSequence.ReplaceAllString(badges, "")), 10)
		fmt.Fprintf(w, "%s  %s\n", st.paint("1", truncateCells(termsafe.Text(a.AgentName), nameCells)), badges)
		for _, line := range entryDetails(a) {
			fmt.Fprintf(w, "%s%s\n", searchIndent, st.paint("2", truncateCells(termsafe.Text(line), lineWidth)))
		}
	}
}

func writeSearchFooter(w io.Writer, result agentsearch.Result, nextCommand string) {
	if nextCommand != "" && len(result.Agents) > 0 {
		fmt.Fprintf(w, "\nMore results: %s\n", termsafe.Text(nextCommand))
	}
	if statusFiltered(result) && len(result.Agents) > 0 {
		fmt.Fprintf(w, "\nShowing %s agents only. %s\n", result.Status, allStatusesHint)
	}
	if !result.Authenticated {
		fmt.Fprintln(w, "\nShowing public agents only. Run 'blocks login' to include your private and shared agents; calling any agent requires a login.")
	}
}

func (st searchStyle) badges(a agentsearch.Agent) string {
	visibility := termsafe.Text(a.Listing)
	if a.Listing == agentsearch.ListingPrivate {
		visibility = st.paint("33", visibility)
	}
	parts := []string{visibility}
	switch a.Availability() {
	case "online":
		parts = append(parts, st.paint("32", "online"))
	case "offline":
		parts = append(parts, st.paint("2", "offline"))
	}
	return strings.Join(parts, " · ")
}

// entryDetails is the display name, unless it only restates the agent name, then the summary unless it repeats that.
func entryDetails(a agentsearch.Agent) []string {
	var lines []string
	if !sameWords(a.DisplayName, a.AgentName) {
		lines = append(lines, a.DisplayName)
	}
	if a.Summary != "" && !sameWords(a.Summary, a.DisplayName) {
		lines = append(lines, a.Summary)
	}
	return lines
}

func sameWords(a, b string) bool {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(s, "_", " "))), " ")
	}
	return norm(a) == norm(b)
}

func writeSearchJSON(w io.Writer, query string, result agentsearch.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Query            string              `json:"query"`
		Authenticated    bool                `json:"authenticated"`
		Agents           []agentsearch.Agent `json:"agents"`
		Next             string              `json:"next,omitempty"`
		SearchParseError bool                `json:"searchParseError,omitempty"`
	}{query, result.Authenticated, result.Agents, result.Next, result.ParseError})
}

func makeAgentSuggestFn(client *blocksapi.Client) wizard.SuggestFunc {
	return func(ctx context.Context, q string) ([]wizard.Suggestion, error) {
		result, err := agentsearch.Search(ctx, client, agentsearch.Query{Text: q, Limit: pickerLimit, Status: agentsearch.StatusAll})
		if err != nil {
			return nil, fmt.Errorf("Search unavailable: %s. You can still type an agent name.",
				searchFailureReason(err, client.BaseURL))
		}
		out := make([]wizard.Suggestion, 0, len(result.Agents))
		for _, a := range result.Agents {
			out = append(out, wizard.Suggestion{Value: a.AgentName, Label: pickerLabel(a)})
		}
		return out, nil
	}
}

func pickerLabel(a agentsearch.Agent) string {
	parts := []string{truncateCells(termsafe.Text(a.DisplayName), pickerDisplayNameMaxCells), termsafe.Text(a.Listing)}
	if status := a.Availability(); status != "" {
		parts = append(parts, status)
	}
	return strings.Join(nonEmpty(parts), " · ")
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func truncateCells(s string, cells int) string {
	return runewidth.Truncate(strings.Join(strings.Fields(s), " "), cells, "…")
}
