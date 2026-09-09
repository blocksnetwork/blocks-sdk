package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

// rootProfile is bound to the persistent --profile flag and resolved in
// PersistentPreRun (after flag parsing) so it wins over BLOCKS_PROFILE and the
// saved active profile.
var rootProfile string

// rootNoInput is bound to the persistent --no-input flag. When set, prompts
// must fail with actionable errors instead of reading stdin.
var rootNoInput bool

// noInputFlagHelp describes --no-input. It names the reads that still reach stdin
// rather than claiming blanket coverage: a flag that promises never to prompt and
// then hangs is worse than one that says where it stops. The documentation points
// here as the live list, so this string has to stay exhaustive and specific.
//
// It has been wrong twice, both times by being vaguer than the code. "deploy
// confirmations" named one site where there were five, and the corrected "three
// 'blocks deploy' reads" still missed the partner token prompt — which was not even
// gated on a terminal. No read under 'blocks deploy' reaches stdin any more, so the
// command leaves this list entirely; prefer closing a read over describing it.
//
// "Closed" is not always "refused": the deploy target joins the non-interactive
// branch and resolves from the positional argument or the saved deployTarget, and
// only errors when neither exists. Of what remains here, one has no answer at all
// (the browser-login organization picker); the partner token prompt is answered by
// exporting the provider's token, not by a flag.
const noInputFlagHelp = "Never prompt: fail with an actionable error naming the flag or variable that answers instead. " +
	"Not yet honoured by the organization picker shown during browser login, " +
	"or 'blocks login --provider cloudflare|vercel|netlify' token prompts"

func init() {
	// Load .env from cwd before any command runs (don't override existing env).
	loadEnvFile(".env")
	rootCmd.PersistentFlags().StringVar(&rootProfile, "profile", "", "Deployment profile to use; also names the profile created by 'blocks login' (overrides BLOCKS_PROFILE and the saved active profile)")
	rootCmd.PersistentFlags().BoolVar(&rootNoInput, "no-input", false, noInputFlagHelp)
}

// envFileKeys records, for each assignment loadEnvFile accepted from a project
// file, the file it came from, keyed by canonicalEnvKey. loadEnvFile only accepts a
// variable the environment did not already carry, so a key present here was supplied
// by that file and a key absent from it was exported by the shell — a distinction
// that no longer exists by the time anything reads os.Getenv. Keeping the record at
// the only point that still knows is what lets a note about an ambient override name
// its source instead of guessing at one.
//
// "Accepted" is wider than "set into this process": the CLI imports only the handful
// of variables it consumes (cliProjectEnvKeys) but hands the whole file to a
// delegated agent runtime, so the record covers both. Every key the CLI itself reads
// is on the allowlist, so the two pin declines — the only consumers of this record —
// see exactly what they saw when it covered imports alone.
//
// It is also the authority on whether an entry is still live. Withdrawing a value
// (declineForeignBackendPin) deletes it from here, and projectEnv() hands a child
// nothing this map has forgotten, so one deletion covers this process and the child.
var envFileKeys = map[string]string{}

// projectEnvEntry is one assignment a project file made, in the spelling the file
// used. The spelling is kept because a delegated agent has to receive the variable
// under the name its own code reads, and on Unix `foo` and `FOO` are two variables.
type projectEnvEntry struct {
	name  string
	value string
}

// projectEnvValues carries the values behind each entry in envFileKeys, keyed the same
// way, so `blocks run` can hand the project file to the delegated agent runtime even
// though this process imports almost none of it.
//
// Every spelling the file used is kept, not just the last, and that is load-bearing
// rather than tidiness. A file assigning both `BLOCKS_BACKEND_URL` and
// `blocks_backend_url` collapses to one canonical provenance entry while remaining two
// distinct process variables on Unix. Keeping only the last made the lowercase
// assignment replace the record for the still-live uppercase value, so both pin declines
// read a hostile file-supplied `BLOCKS_BACKEND_URL` as shell-exported and waved it
// through — a project file could then aim a flag- or stdin-supplied key at an arbitrary
// collector. On Unix the child also genuinely wants both, since they are two variables.
var projectEnvValues = map[string][]projectEnvEntry{}

// projectEnv returns the live assignments a project file supplied, sorted by name so
// a child's environment does not depend on map iteration order. An entry is live only
// while envFileKeys still records it, which is what makes a withdrawal single-sited.
func projectEnv() []projectEnvEntry {
	out := make([]projectEnvEntry, 0, len(projectEnvValues))
	for key, entries := range projectEnvValues {
		if envFileKeys[key] == "" {
			continue
		}
		out = append(out, entries...)
	}
	slices.SortFunc(out, func(a, b projectEnvEntry) int { return strings.Compare(a.name, b.name) })
	return out
}

// projectEnvValueFor returns the value a project file supplied for the canonical key,
// taking the last spelling when a file used more than one — which is what a delegated
// agent reading the canonical name would see on a case-insensitive platform.
func projectEnvValueFor(canonical string) string {
	entries := projectEnvValues[canonical]
	if len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1].value
}

// dropProjectEnvValue withdraws a value a project file supplied. It leaves this
// process's environment, the provenance record, and — through that record — the
// environment handed to a delegated agent runtime.
//
// All three, because a decline that only unset the variable here would be undone by
// the child merge: `blocks run` re-applies the project file on top of os.Environ()
// precisely so an agent's own configuration survives an allowlist, and a declined pin
// would ride along with it into the runtime the pin was aimed at.
//
// The key folds through canonicalEnvKey, so a file that pinned the lowercase spelling
// of a variable on Unix cannot leave the value behind for the child under a name the
// decline did not think to look at.
func dropProjectEnvValue(key string) {
	os.Unsetenv(key)
	canonical := canonicalEnvKey(key)
	for _, entry := range projectEnvValues[canonical] {
		os.Unsetenv(entry.name)
	}
	delete(envFileKeys, canonical)
	delete(projectEnvValues, canonical)
}

// canonicalEnvKey folds a variable name to the single spelling everything in this
// package reasons about, so the provenance record, the allowlist and the note list
// cannot disagree about what a variable is called.
//
// It folds unconditionally rather than per-platform. The question the provenance
// record answers is "could a project file have supplied the value os.Getenv(name)
// now returns", and on Windows — a shipped platform; see run_windows.go — the
// answer is yes whatever case the file used, because the process environment is
// case-insensitive there: a `.env` writing blocks_backend_url is visible to
// os.Getenv("BLOCKS_BACKEND_URL"), and a record keyed by the file's own spelling
// would report that value as shell-exported and wave it through both pin declines.
// Folding only on Windows would leave that fold unexercised on the platform most
// of this is developed and tested on, which is how it would rot.
//
// On Unix, FOO and foo really are different variables, so folding here means a file
// assigning the lowercase spelling lends provenance to the canonical name — a variable
// it did not supply. That used to be described as erring safely towards declining an
// override. It did not: a decline unsets the canonical variable, so a value the shell
// exported under it was discarded and the project file was named as its origin, which
// silently retargeted the command and misattributed the reason.
//
// The keying stays folded, because that is what makes the record findable on Windows.
// What changed is that the declines ask envFileSuppliedCLIVar, which additionally
// requires the recorded entry's spelling to be the variable this platform has the CLI
// read. So on Unix only an exact `BLOCKS_BACKEND_URL` in the file can be declined, and
// a shell export always survives.
func canonicalEnvKey(key string) string { return strings.ToUpper(key) }

// envFileSource names the file loadEnvFile read the given variable from, or ""
// when the variable did not come from a project .env and its origin is therefore
// not something the CLI can observe.
func envFileSource(key string) string { return envFileKeys[canonicalEnvKey(key)] }

// envNamesAreCaseInsensitive is true on the platforms whose process environment folds
// case, which is Windows. It is a variable rather than an inlined runtime.GOOS test so a
// test can model the other platform's semantics: the Windows path is the one a Unix
// developer cannot otherwise reach, and leaving it unexercised is how it rots.
var envNamesAreCaseInsensitive = runtime.GOOS == "windows"

// sameEnvVarHere reports whether two environment-variable spellings name the same
// variable on this platform. Windows environments are case-insensitive, so any casing
// matches; on Unix `foo` and `FOO` are two variables and only an exact match does.
func sameEnvVarHere(a, b string) bool {
	if envNamesAreCaseInsensitive {
		return canonicalEnvKey(a) == canonicalEnvKey(b)
	}
	return a == b
}

// envFileSuppliedCLIVar names the file that supplied the CLI variable `key`, and "" when
// no project-file entry supplied the variable this platform would have the CLI read under
// that name.
//
// This is the question the two pin declines have to ask, and envFileSource is not it.
// Provenance is recorded under the canonical spelling so Windows consumers can find it,
// which means a Unix file assigning `blocks_backend_url` records provenance for
// BLOCKS_BACKEND_URL — a variable it did not supply, because on Unix those are two
// different names. A decline reading envFileSource then saw a shell-exported
// BLOCKS_BACKEND_URL as file-sourced, and dropProjectEnvValue unset the shell's export
// and named the project file as its origin. The export is the supported headless
// mechanism and is meant to be authoritative, so that silently retargeted the command
// and misattributed the reason.
func envFileSuppliedCLIVar(key string) string {
	canonical := canonicalEnvKey(key)
	// Any spelling, not the last one: a file that assigned both spellings supplied the
	// variable the CLI reads whichever order it wrote them in, and asking only about the
	// most recent entry is what let a trailing lowercase line hide a hostile uppercase pin.
	for _, entry := range projectEnvValues[canonical] {
		if sameEnvVarHere(entry.name, key) {
			return envFileKeys[canonical]
		}
	}
	return ""
}

// displayEnvPath renders a .env path the way a user would type it, so a message
// naming the file is unambiguous about which directory it means.
func displayEnvPath(path string) string {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") {
		return path
	}
	return "./" + path
}

// blocksProfileEnv and blocksCLIClientIDEnv name two variables this CLI resolves from
// its environment whose spelling lives outside this file: internal/profiles reads
// BLOCKS_PROFILE (SelectedName, Active) and resolveClientID reads
// BLOCKS_CLI_CLIENT_ID. They are restated here because cliProjectEnvKeys has to be
// complete, and TestEveryBlocksEnvironmentVariableIsClassified is what stops a
// restatement from drifting away from the reader.
const (
	blocksProfileEnv     = "BLOCKS_PROFILE"
	blocksCLIClientIDEnv = "BLOCKS_CLI_CLIENT_ID"
)

// cliProjectEnvKeys is the complete set of variables this CLI's own process accepts
// from a project file, in the canonical uppercase spelling the membership test
// compares against. Every one of them is a variable the CLI resolves from its
// environment at runtime, and nothing else is imported at all.
//
// It is an allowlist because the denylist it replaces was found incomplete three
// times. The category that list tried to name — "reconfigures the process, or
// relocates state the CLI trusts, rather than configuring the application" — is not
// enumerable: transport and TLS trust (HTTPS_PROXY, SSL_CERT_FILE, GODEBUG) were the
// first miss, state location (XDG_CONFIG_HOME, HOME, USERPROFILE) the second, and
// execution control the third — LD_PRELOAD, DYLD_INSERT_LIBRARIES, NODE_OPTIONS,
// PYTHONPATH and BASH_ENV all run attacker code in a process this CLI starts, and
// BLOCKS_INSTALL_DIR chooses where `blocks upgrade` writes the next binary. A fourth
// category exists; enumerating this side instead means it does not matter.
//
// The reason the earlier reading rejected an allowlist was that `blocks run` built the
// delegated agent's environment from os.Environ() alone, so filtering here would have
// stripped every scaffolded agent's own configuration. buildChildEnv now merges the
// whole project file into the child instead, which is what makes this safe: the
// variables leaving this set are still delivered to the process they were meant for.
//
// PATH is worth naming for what is no longer needed: the denylist left it out on the
// grounds that a file could never supply it, because the loader skips any variable the
// environment already carries and PATH always is. That held for a login shell and not
// for an `env -i` invocation, a launchd job, or a scratch container. An allowlist owes
// no such argument.
var cliProjectEnvKeys = map[string]bool{
	blocksAPIKeyEnv:       true,
	blocksBackendURLEnv:   true,
	blocksAppBaseURLEnv:   true,
	blocksDashboardURLEnv: true,
	cdm.URLEnv:            true,
	blocksProfileEnv:      true,
	blocksCLIClientIDEnv:  true,
}

// cliAcceptsFromProjectEnv reports whether this process imports the given variable
// from a project file. The comparison folds through canonicalEnvKey, the same helper
// the provenance record uses, so the two cannot come to different conclusions about
// which variable a line names — and on Windows, where the process environment is
// case-insensitive, a lowercase spelling really is the variable the CLI reads.
func cliAcceptsFromProjectEnv(key string) bool {
	return cliProjectEnvKeys[canonicalEnvKey(key)]
}

// explainedWithheldKeys names the variables whose withholding is worth a message,
// because a developer who set one deliberately would otherwise be left with a CLI
// that appears to ignore it.
//
// This list decides nothing and is not a security boundary — cliProjectEnvKeys is.
// A name missing from here costs a user an explanation, not an interception, which is
// the entire difference between it and the denylist it grew out of.
//
// It covers the three categories the denylist accumulated, plus one it never reached.
// Transport and TLS trust: how the CLI reaches the network, whose certificates it
// believes, and which retired-insecure behaviours GODEBUG re-enables. State location:
// XDG_CONFIG_HOME and the home variables under it decide where the profile store, the
// legacy credential store, the deploy-plugin directory and the CDM cache live, and the
// profile store is the only evidence declineForeignBackendPin has. Hosting-provider
// credentials: `blocks deploy` resolves CLOUDFLARE_API_TOKEN, VERCEL_TOKEN and
// NETLIFY_AUTH_TOKEN from the environment, and a file that supplied one would choose
// the account a user's assets are published into; the documented way to set them is an
// export, so withholding them costs nothing the docs promised. And BLOCKS_INSTALL_DIR,
// which names the directory `blocks upgrade` writes a freshly downloaded binary into.
var explainedWithheldKeys = map[string]bool{
	"HTTP_PROXY":            true,
	"HTTPS_PROXY":           true,
	"ALL_PROXY":             true,
	"NO_PROXY":              true,
	"SSL_CERT_FILE":         true,
	"SSL_CERT_DIR":          true,
	"GODEBUG":               true,
	"XDG_CONFIG_HOME":       true,
	"HOME":                  true,
	"USERPROFILE":           true,
	"CLOUDFLARE_API_TOKEN":  true,
	"CLOUDFLARE_ACCOUNT_ID": true,
	"VERCEL_TOKEN":          true,
	"VERCEL_TEAM_ID":        true,
	"NETLIFY_AUTH_TOKEN":    true,
	"BLOCKS_INSTALL_DIR":    true,
}

// worthExplainingWithheld reports whether withholding the given variable from this
// process deserves a note. Ordinary application variables do not: they are the normal
// contents of a project file, they still reach the agent that reads them, and a line
// about each would bury the ones that matter.
func worthExplainingWithheld(key string) bool {
	return explainedWithheldKeys[canonicalEnvKey(key)]
}

// loadEnvFile imports the project file at path into this process, subject to the
// allowlist, and records what it saw for the delegated-agent environment and the
// provenance notes.
//
// Each line is parsed by auth.EnvLineAssignment, which is also the parse the .env
// *writer* uses to decide which line assigns a variable and what a variable's value is.
// Calling it rather than restating it is what makes the two sides one rule: the writer
// quotes a value that needs quoting, and quoted is only a faithful representation of the
// value if the reader takes the quotes off again. Two implementations of that would be
// two chances for a credential to be read back with a quote still attached, which
// authenticates nowhere and explains nothing.
func loadEnvFile(path string) {
	// The record describes the load that is about to happen, not an earlier one: in
	// production this runs exactly once, and clearing keeps the invariant "recorded ⇒
	// this file supplied it" true if it ever runs again.
	clear(envFileKeys)
	clear(projectEnvValues)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var withheld []string
	source := displayEnvPath(path)
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := auth.EnvLineAssignment(line)
		if !ok {
			continue
		}
		if os.Getenv(k) != "" {
			continue
		}
		// Skip empty assignments to avoid shadowing downstream resolution.
		// An empty .env value carries no information, but setting the variable
		// to "" defeats injection mechanisms that check for variable presence.
		if v == "" {
			continue
		}
		// Recorded under the canonical spelling, not the file's: on Windows the two
		// name the same variable, so provenance has to be findable by the name every
		// consumer asks for. The value keeps the file's spelling, because that is the
		// name a delegated agent reads it under.
		canonical := canonicalEnvKey(k)
		envFileKeys[canonical] = source
		projectEnvValues[canonical] = append(projectEnvValues[canonical], projectEnvEntry{name: k, value: v})
		if !cliAcceptsFromProjectEnv(k) {
			// Checked after the two skips above so the note describes only variables
			// that would otherwise have taken effect here: a variable the shell already
			// exported is in effect from the shell, and an empty assignment was never
			// going to be imported, so neither is something a user needs explained.
			if worthExplainingWithheld(k) {
				withheld = append(withheld, k)
			}
			continue
		}
		os.Setenv(k, v)
	}
	if len(withheld) > 0 {
		fmt.Fprint(os.Stderr, withheldEnvFileNote(withheld, source))
	}
}

// withheldEnvFileNote words the withholding: which variables, the file that supplied
// them, why this process does not take them from a file, and the remedy — export them
// in the shell, where they read as the operator's own choice rather than as something
// a checked-in file decided.
//
// It borrows the shape of the pin declines (indented, the variables named, the source
// named only because loadEnvFile knows it) for the same reason: a developer who put a
// proxy in .env deliberately would otherwise be left with a CLI that quietly ignores
// it. The variables are named in the spelling the file used, so the offending line is
// findable, and sanitized because that spelling is attacker-chosen text. Their values
// are not echoed: they are attacker-chosen too, and nothing about the remedy needs them.
//
// The last two lines read as something to run — they say to export the variables — and
// they name them, which is the one interpolation this file permits into a line like
// that. It is safe for a reason worth stating, because the general rule is the opposite:
// a name only reaches here after worthExplainingWithheld matched its uppercase fold
// against explainedWithheldKeys, so it is a case variant of one of those fixed names and
// cannot carry a `;`, a backtick or a $(...) at all. Every other value in this
// package — a profile name, a declined pin, an agent name — has no such guarantee and is
// kept off a command line entirely.
//
// The last line is there because it is the difference between this note and the one it
// replaces. These variables are withheld from the CLI, not discarded: `blocks run`
// still hands them to the agent, so a developer whose agent needs PYTHONPATH or
// NODE_OPTIONS should not go looking for another mechanism.
//
// It goes to stderr because the load runs before any command body, for every command,
// including the ones whose stdout is a machine-readable document.
func withheldEnvFileNote(keys []string, source string) string {
	safe := make([]string, 0, len(keys))
	for _, k := range keys {
		safe = append(safe, termsafe.Text(k))
	}
	remedy, passes := "export them in your shell", "passes them"
	if len(safe) == 1 {
		remedy, passes = "export it in your shell", "passes it"
	}
	named := strings.Join(safe, ", ")
	return fmt.Sprintf("  Not importing %s from %s — this CLI takes only its own settings from a project file, so a cloned repository cannot change how it reaches the network, which certificates it trusts, where it keeps your credentials, or which hosting account it deploys to.\n",
		named, termsafe.Text(source)) +
		fmt.Sprintf("  To use %s for the CLI itself, %s. 'blocks run' still %s to your agent.\n", named, remedy, passes)
}

var rootCmd = &cobra.Command{
	Use:     "blocks",
	Version: Version,
	Short:   "Blocks CLI",
	Long: `Blocks CLI — build and manage AI agents.

Quick start:
  blocks init my_agent                    Scaffold a new agent (provider) project
  blocks init my_consumer --mode consumer Scaffold a new consumer project
  cd my_agent && blocks login --write-env Authenticate (first time only)
  blocks register                         Register the agent privately and free (recommended first step)
  blocks run                              Start the agent locally
  blocks publish                          Later: make the agent public or set pricing

Authentication & publishing:
  blocks login     Authenticate and store API credentials
  blocks register  Register an agent privately and free (recommended first step)
  blocks publish   Publish an agent — public/private, free/paid (requires prior login)
  blocks logout    Remove stored credentials
  blocks whoami    Show current identity

Dashboard:
  blocks dashboard  Open the agent dashboard`,
	SilenceUsage:  true,
	SilenceErrors: true,
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
	// PersistentPreRunE, not PersistentPreRun, so a store that cannot be read stops
	// the invocation as a returned error that main() reports like any other. The
	// non-error variant forced an os.Exit(1) from inside the hook, which no test
	// could observe without a package-level seam standing in for the exit.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Branding is set once here, AFTER flag parsing, so --profile wins over
		// BLOCKS_PROFILE and the saved active profile. Static Short/Long strings
		// render before this hook and so cannot reflect runtime branding — they
		// are kept brand-neutral instead.
		if rootProfile != "" {
			profiles.SetActiveOverride(rootProfile)
		}
		// First, because everything below reads the store and none of it can tell a
		// store that cannot be read apart from one that trusts nothing.
		store, err := loadProfileStore()
		if err != nil {
			return err
		}
		// Every profiles.Active() below and in the command bodies that follow runs
		// after that gate, so an error from one can no longer mean "unreadable store".
		// What is left is --profile or BLOCKS_PROFILE naming a profile that does not
		// exist, and that is refused here rather than left to each command.
		if err := requireSelectedProfileExists(cmd); err != nil {
			return err
		}
		if _, p, err := profiles.Active(); err == nil && p.ProductName != "" {
			branding.Set(p.ProductName)
		}
		// Before the target is resolved, because the resolver latches the ambient
		// backend URL and every credential tier is read against it: a value declined
		// after that point would already have decided where the credential goes.
		declineForeignBackendPin(store)
		// Local-only; the enterprise question is answered lazily by clictx so
		// this adds no network round-trip to any command.
		clictx.Resolve(effectiveOverrides(cmd))
		// Directly after the target is resolved, because it needs the answer, and
		// before any command body can call cdm.Get(), whose result is memoized for
		// the rest of the process.
		declineForeignCDMPin()
		// Propagate --no-input flag to prompt primitives
		setNoInputMode(rootNoInput)
		wizard.SetNoInputMode(rootNoInput)
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if cmd.Name() != "upgrade" {
			checkForUpdateNotice()
		}
	},
}

// effectiveOverrides gathers every input that can displace the active profile as
// this invocation's request target, so clictx can resolve the target and the
// credential once and every consumer can read that one answer. It performs no
// ordering of its own: it hands the resolver the ambient backend URL, the
// build-time default, and each credential tier separately, and the resolver owns
// which one wins.
//
// The credential flags are read off the command being run — cobra has already
// parsed them by the time PersistentPreRun executes — rather than from each
// command's own flag variable. That is why no per-command injection step is
// needed: `cmd.Flags().Lookup` is a command-agnostic view of command-scoped
// flags, so a command that gains --api-key later cannot silently fall out of the
// resolution, and a command that never had it is simply a nil lookup.
//
// --api-key-stdin contributes only its presence plus the reader to use. Reading
// the value here would consume the key inside a hook that runs for every command,
// so the read itself stays behind clictx.EffectiveCredential's one-shot memoization.
func effectiveOverrides(cmd *cobra.Command) *clictx.Overrides {
	o := &clictx.Overrides{
		BackendURL:          strings.TrimSpace(os.Getenv(blocksBackendURLEnv)),
		DefaultBackendURL:   defaultBackendURL,
		EnvCredential:       strings.TrimSpace(os.Getenv(blocksAPIKeyEnv)),
		ReadStdinCredential: func() (string, error) { return readAPIKeyFromStdin() },
		StoredCredential:    loadStoredCredential,
	}
	if f := cmd.Flags().Lookup("api-key"); f != nil {
		o.FlagCredential = strings.TrimSpace(f.Value.String())
	}
	if f := cmd.Flags().Lookup("api-key-stdin"); f != nil && f.Value.String() == "true" {
		o.CredentialFromStdin = true
	}
	return o
}

// declineForeignBackendPin drops a BLOCKS_BACKEND_URL that only a project .env
// supplied and that names a deployment the user has no established relationship
// with — one absent from the profile store.
//
// It is the same rule as declineForeignCDMPin, for a stronger reason: this variable
// *is* the request origin, so every command that sends a credential — publish,
// register, unregister, invite, the login paths — sends it wherever this value
// points. A `.env` arrives with a cloned repository, so cloning a hostile repo and
// running any authenticated command in it was enough to hand over an API key,
// including one passed explicitly as --api-key. loadEnvFile records which of a file
// and the shell supplied a value, and that record is the whole distinction here.
//
// What makes a pin dangerous is not that it disagrees with the *active* profile.
// Disagreeing with it is the entire point of a project-local pin: `blocks login
// --write-env` leaves one in the directory it was run in precisely so that directory
// keeps acting on its own deployment after `blocks profile use` selects another, and
// the context banner names that deployment because that is where the command will
// act. What is dangerous is a pin naming a deployment the user never logged in to,
// because then the credential goes somewhere they have no relationship with at all.
// So the pin is checked against every profile's deployment, not just the active
// one — a profile exists only because someone deliberately authenticated to it — and
// a pin naming a deployment the user does trust is honoured however many profiles are
// in the store. That is why an attacker gains nothing by naming one: the key ends up
// at a deployment its owner already logged in to.
//
// A value the shell or the CI job exported is intent and is never touched, whatever
// the profile store holds: the scripted BLOCKS_BACKEND_URL + BLOCKS_API_KEY path is
// the supported headless mechanism and must keep outranking the profiles entirely.
// A declined value is withdrawn rather than merely ignored, because the variable has
// more readers than the resolver: helpers resolve it from this process, and a
// delegated agent runtime is handed this process's environment with the project file
// merged back over it. dropProjectEnvValue covers both.
//
// This is the single choke point, deliberately. Guarding the credential tiers instead
// would mean teaching the resolver about `.env` provenance it has no other reason to
// know, and leave two copies of one rule to drift apart; dropping the variable at the
// boundary means every consumer — resolver, helpers, child process — sees the same
// environment, and none of them needs the rule at all.
func declineForeignBackendPin(store *profiles.Contexts) {
	pinned := strings.TrimSpace(os.Getenv(blocksBackendURLEnv))
	source := envFileSuppliedCLIVar(blocksBackendURLEnv)
	if pinned == "" || source == "" {
		return
	}
	if knownDeployment(store, pinned) {
		return
	}
	dropProjectEnvValue(blocksBackendURLEnv)
	fmt.Fprint(os.Stderr, declinedBackendPinNote(pinned, source, fallbackTargetName()))
}

// knownDeployment reports whether any saved profile in store describes the given
// deployment. A profile exists only because someone logged in to that deployment, so
// the store is the CLI's record of which origins the user has deliberately trusted
// with a credential.
//
// The store is passed in rather than loaded here. The load can fail, and a failure is
// not the same fact as "no deployments are trusted" even though both leave this
// function with nothing to match against — so the decision about what a failed load
// means belongs at the one place that can act on it (loadProfileStore, called before
// any of this runs) and not at a call site whose only vocabulary is true and false.
// A nil store is the legitimately-empty case: no contexts.json has ever been written.
//
// "Same deployment" is asked of profiles.SameBaseURL rather than compared as strings,
// so a trailing slash or a default port cannot make an ordinary pin look foreign.
// SameBaseURL is false for an empty base URL, so the stock Blocks Network profile —
// which records no deployment of its own — never vouches for anything.
func knownDeployment(store *profiles.Contexts, rawURL string) bool {
	if store == nil {
		return false
	}
	for _, p := range store.Profiles {
		if profiles.SameBaseURL(rawURL, p.BaseURL) {
			return true
		}
	}
	return false
}

// loadProfileStore reads the deployment profile store, separating "never written, so
// genuinely nothing is trusted" from "present and unreadable, which is a failure".
//
// The two are indistinguishable to a caller that only looks at how many profiles came
// back, and collapsing them is dangerous in a specific way: the emptiness of that list
// is the only evidence declineForeignBackendPin has. A corrupt or unreadable
// contexts.json makes every project pin look foreign, including the ordinary one
// `blocks login --write-env` leaves behind, so the pin is dropped and resolution falls
// through to the default deployment — while the invocation still carries the API key
// the same project file supplied, which was minted somewhere else. Silently retargeting
// a credential is not an acceptable response to a config file the CLI cannot read.
//
// Absence returns a nil store rather than an error: a user who has never logged in has
// no contexts.json, and nothing is wrong. profiles.Load answers that case itself by
// synthesising a store, so the fs.ErrNotExist branch here is the belt to that braces —
// it costs one comparison and means a future store that reports absence as an error
// does not turn every first run into a failure.
//
// Any other error names the file, because the remedy is to look at it or delete it, and
// an error that only says the store could not be read leaves a user hunting for which
// store.
func loadProfileStore() (*profiles.Contexts, error) {
	c, err := profiles.Load()
	if err == nil {
		return c, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	path, pathErr := profiles.ContextsPathFunc()
	if pathErr != nil || path == "" {
		return nil, fmt.Errorf("cannot read the deployment profile store: %w", err)
	}
	return nil, fmt.Errorf("cannot read the deployment profile store %s: %w", path, err)
}

// fallbackTargetName names the deployment an invocation falls back to once a pin is
// declined: the active profile's own, else the public deployment. It is resolved from
// the profile store rather than from clictx because the decline runs before the
// context is resolved — which is the whole point of where it sits.
func fallbackTargetName() string {
	if _, p, err := profiles.Active(); err == nil && p.BaseURL != "" {
		return hostSlug(p.BaseURL)
	}
	return branding.Default()
}

// declinedBackendPinNote words the decline: which variable, the file that supplied
// it, the deployment used instead, and the two ways to keep it — log in to the
// deployment it names, or export it so it reads as a deliberate choice rather than
// as something a checked-in file decided.
//
// It goes to stderr because it is emitted before the command body from a hook that
// runs for every command, including the ones whose stdout is a machine-readable
// document.
// The remedy names the command but never carries the declined value into it. The two
// protections are orthogonal and both are needed: termsafe.Text neutralises terminal
// control sequences, and no amount of it makes a value safe to paste into a shell,
// because `;`, `&&`, backticks and $(...) are ordinary characters that survive it
// intact. A line presented as something to copy is a line that will be copied, so the
// only value in it is the user's own — they know the deployment they meant, and
// echoing the attacker's is what created the hazard.
func declinedBackendPinNote(pinned, source, target string) string {
	// The declined value and the file that supplied it are both attacker-chosen text
	// on this path, so neither may reach the terminal unsanitized.
	return fmt.Sprintf("  Not using %s=%s in %s — you are not logged in to that deployment, so %s is the target instead.\n",
		blocksBackendURLEnv, termsafe.Text(pinned), termsafe.Text(source), termsafe.Text(target)) +
		fmt.Sprintf("  To use it, log in to that deployment with 'blocks login', or export %s in your shell.\n", blocksBackendURLEnv)
}

// declineForeignCDMPin drops a BLOCKS_CDM_URL that only a project .env supplied
// and that does not name the CDM endpoint of the deployment this invocation
// targets.
//
// The CDM payload carries api.baseUrl, and a login with nothing else to go on
// resolves its backend from it, so this variable can redirect a credential exactly
// as BLOCKS_BACKEND_URL can — which is why it is held to the same rule, for the
// same reason. A .env arrives with a cloned repository and is therefore weaker
// evidence of intent than an export, and loadEnvFile records which of the two
// supplied a value. `blocks login --write-env` writes this variable itself, so
// finding it in a project .env is ordinary; what separates an ordinary one from a
// hostile one is whether it agrees with the effective target.
//
// A value the shell or the CI job exported is intent and is never touched. A
// file-sourced value naming the effective target's own CDM endpoint is what
// --write-env left behind in its own project directory, and is honoured silently.
// Anything else is unset rather than merely ignored, because the variable has two
// readers: the CLI's own CDM fetch resolves it from this process, and a delegated
// agent runtime is handed this process's environment with the project file merged back
// over it.
//
// The mapping from a deployment origin to its CDM endpoint is asked of
// cdm.EndpointFor rather than spelled out again here; a second spelling of that
// path is how the two answers drift apart. It yields "" when no deployment was
// named — the stock Blocks Network case, whose CDM is the built-in default — and
// no file-sourced value can match that, so every such value is declined.
func declineForeignCDMPin() {
	pinned := strings.TrimRight(strings.TrimSpace(os.Getenv(cdm.URLEnv)), "/")
	source := envFileSuppliedCLIVar(cdm.URLEnv)
	if pinned == "" || source == "" {
		return
	}
	// EndpointFor normalizes its own output, so both sides are compared trimmed.
	if want := cdm.EndpointFor(clictx.ChosenBackendURL()); want != "" && pinned == want {
		return
	}
	dropProjectEnvValue(cdm.URLEnv)
	fmt.Fprint(os.Stderr, declinedCDMPinNote(pinned, source))
}

// declinedCDMPinNote words the decline: which variable, the file that supplied it,
// the deployment it disagrees with, and the two ways to keep it — name the
// deployment that serves it, or export it so it reads as a deliberate choice.
//
// It borrows the shape of the ambient-override notes (indented, the value quoted
// back, the source named only because loadEnvFile knows it) but not their neutral
// register: those explain a supported mechanism taking effect, whereas this one is
// declining to follow an input, and a user who cannot tell the difference will go
// looking for the CDM they thought they had set.
//
// It goes to stderr, unlike those notes, because it is emitted before the command
// body from a hook that runs for every command — including the ones whose stdout is
// a machine-readable document.
func declinedCDMPinNote(pinned, source string) string {
	target := branding.Default()
	if chosen := clictx.ChosenBackendURL(); chosen != "" {
		target = hostSlug(chosen)
	}
	// Both lines name the file, and both sanitize it: one unsanitized copy of a value
	// is as good as none. Neither line carries the declined value into a command, for
	// the reason declinedBackendPinNote spells out.
	return fmt.Sprintf("  Not using %s=%s in %s — %s does not serve that CDM endpoint.\n",
		cdm.URLEnv, termsafe.Text(pinned), termsafe.Text(source), termsafe.Text(target)) +
		fmt.Sprintf("  To use it, pin the deployment that does (%s in %s), or export %s in your shell.\n",
			blocksBackendURLEnv, termsafe.Text(source), cdm.URLEnv)
}

func Execute() error {
	return rootCmd.Execute()
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not get working directory: %s\n", err)
		os.Exit(1)
	}
	return cwd
}

// selectedProfileName is the profile this invocation was explicitly told to use, and ""
// when it was told nothing. It is deliberately not "which profile is active": the saved
// active profile and the built-in default always exist (ensureDefault materializes the
// latter), so only an explicit selection can name something that does not.
func selectedProfileName() string {
	if rootProfile != "" {
		return rootProfile
	}
	return strings.TrimSpace(os.Getenv(blocksProfileEnv))
}

// profileMayNameANewOne reports whether cmd is allowed to name a profile the store does
// not have yet. `blocks login --profile acme` creates it — that is what the flag's own
// help promises — and the `blocks profile` subcommands manage the store itself, so
// neither can be held to "it must already exist".
func profileMayNameANewOne(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "login", "profile":
			return true
		}
	}
	return false
}

// requireSelectedProfileExists refuses an explicit --profile / BLOCKS_PROFILE that names
// no saved profile, for every command that reads one rather than creates one.
//
// Silently ignoring it was worse than it sounds. The resolver drops the lookup error and
// carries on, so a mistyped name did not select nothing — it fell through to the next
// tier: an ambient backend URL, the legacy credential store, the linker default, or a
// redirected CDM endpoint. `blocks unregister --profile prod-typo` would then delete an
// agent on whatever deployment those tiers named, having been told to act on one the
// user believed was `prod`. Reporting it costs a mistyped flag an error message; ignoring
// it costs an irreversible action on an unintended deployment.
func requireSelectedProfileExists(cmd *cobra.Command) error {
	name := selectedProfileName()
	if name == "" || profileMayNameANewOne(cmd) {
		return nil
	}
	// Looked up in the store directly rather than through Active(), which answers from
	// the override recorded by SetActiveOverride. Depending on that would make this check
	// correct only when it runs after that call, and silently vacuous anywhere else.
	store, err := profiles.Load()
	if err != nil {
		return err
	}
	if _, ok := store.Profiles[name]; !ok {
		// The name is interpolated on its own line, away from the command, because a
		// line that reads as something to paste gets pasted and this value comes from a
		// flag or the environment. %q also escapes any control characters it carries.
		return fmt.Errorf("profile %q not found\nRun 'blocks profile list' to see which profiles exist", name)
	}
	return nil
}
