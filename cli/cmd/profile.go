package cmd

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(profileCmd)
	profileCmd.AddCommand(profileListCmd, profileUseCmd, profileRenameCmd, profileRemoveCmd)
}

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage deployment profiles (Blocks Network / Enterprise instances)",
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := profiles.Load()
		if err != nil {
			return err
		}
		names := make([]string, 0, len(c.Profiles))
		for n := range c.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		// The note is attached to the profile clictx resolved against, because that
		// is the profile the sentence "overrides this" is about. The trailing print
		// covers the case where no listed row matched, so the note can never be
		// silently dropped.
		note := backendOverrideNoteLine("  ")
		for _, n := range names {
			marker := "  "
			if n == c.Active {
				marker = "* "
			}
			p := c.Profiles[n]
			target := p.BaseURL
			if target == "" {
				target = "Blocks Network (default)"
			}
			fmt.Printf("%s%s  %s\n", marker, n, target)
			if n == clictx.Profile() {
				fmt.Print(note)
				note = ""
			}
		}
		fmt.Print(note)
		return nil
	},
}

var profileUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Switch the active profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _, err := profiles.SetActive(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Active profile: %s\n", name)
		// Switching profiles changed one of the resolver's inputs, so the context
		// resolved before this command ran describes the profile that was active a
		// moment ago. Re-resolving — local only, no network — is what makes the note
		// true of the profile just selected. Someone who switches profiles is
		// usually trying to change where commands go, which is precisely when being
		// told that an override still decides that matters most.
		clictx.Reset()
		clictx.Resolve(effectiveOverrides(cmd))
		fmt.Print(backendOverrideNoteLine("  "))
		return nil
	},
}

var profileRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a profile",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := profiles.Rename(args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("Renamed profile: %s -> %s\n", args[0], args[1])
		return nil
	},
}

var profileRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runProfileRemove(args[0], ".env")
	},
}

// runProfileRemove forgets a deployment: the profile that describes it, and everything
// the project .env holds for it.
//
// The order is what makes the two halves safe to attempt. The dangerous end state is
// the profile deleted while the .env still carries its targeting and its key: the user
// is then holding a live credential for a deployment `blocks profile list` no longer
// mentions, and has been told the removal succeeded, so has no reason to look. So the
// removal is planned against the store first — a name that cannot be removed fails
// before the file is touched — the .env is cleaned second, and only a .env this command
// could read and rewrite lets the profile go. Every failure up to that point leaves
// both halves exactly as they were, and says which profile was not removed.
//
// Two files cannot be written as one transaction, so one partial outcome survives: the
// .env cleaned and the store rewrite failing. That is the harmless direction — no
// credential outlives its profile — and it is reported as what it is, along with the
// same command to run again, which finds nothing left in the .env and retries the
// profile.
//
// The ordering below must not be read as the .env decision being made here. What the
// removal is allowed to delete is re-decided against the store inside the cleanup step,
// immediately before the file is written; the plan this starts with settles the refusals
// and nothing else that is acted on.
func runProfileRemove(name, envPath string) error {
	removal, err := planProfileRemoval(name)
	if err != nil {
		return err
	}
	cleaned, err := removeDeploymentFromProjectEnv(envPath, removal)
	if err != nil {
		return err
	}
	if err := profiles.Remove(name); err != nil {
		if len(cleaned) > 0 {
			// The retry is offered as a command to run, so it carries no value the
			// caller did not type: a profile name reaches the store from the host of
			// whatever deployment a login reached, and that host can come from a
			// project .env or from a deployment's own discovery response. %q makes the
			// name safe to display and says nothing about whether it is safe to run —
			// `;`, `&&`, backticks and $(...) are ordinary printable characters that
			// survive both %q and termsafe.Text — and a line presented as something to
			// paste gets pasted. So the name is reported on the first line, where it is
			// read, and the second names the command and leaves the value to the user.
			return fmt.Errorf("removed %s from %s, but profile %q is still listed: %w\n"+
				"  To finish, run: blocks profile remove <profile>  # the profile named above",
				listAnd(cleaned), displayEnvPath(envPath), name, err)
		}
		return err
	}
	fmt.Printf("Removed profile: %s\n", name)
	if len(cleaned) > 0 {
		subject := "it belonged to"
		if len(cleaned) > 1 {
			subject = "they belonged to"
		}
		fmt.Printf("  Removed %s from %s — %s the deployment you just removed.\n",
			listAnd(cleaned), displayEnvPath(envPath), subject)
	}
	return nil
}

// profileRemoval is what removing a profile has to forget, read out of the store before
// anything is written: the profile's name, the deployment it targets ("" for stock
// Blocks Network), whether that deployment outlives the removal, and the API keys the
// profile cached that no surviving profile also holds. All of it is read here because
// its only use is a comparison against the project .env, and once the profile is gone
// there is nothing left to ask.
type profileRemoval struct {
	name    string
	baseURL string
	// deploymentSurvives is true when some other profile also describes baseURL, so
	// this removal forgets one of a deployment's names and not the deployment — see
	// envKeysBoundToDeployment for what that means for the project .env.
	deploymentSurvives bool
	// ownKeys are the removed profile's cached API keys, less any key another profile
	// also holds — see envKeysBoundToDeployment for what that identity proves.
	ownKeys []string
}

// planProfileRemoval reads what removing name would forget, and refuses — before
// anything at all is written — a removal profiles.Remove would refuse anyway.
//
// The two refusals are deliberately spelled out twice. profiles.Remove owns both rules
// and re-checks them, but it can only answer after the project .env has already been
// rewritten, and a .env stripped for a removal that never happened is worse than a
// duplicated check. TestProfileRemoveRefusesExactlyWhatTheStoreRefuses holds the two
// answers together so they cannot drift.
//
// It is called twice per removal: once up front, for the refusals and to decide whether
// the .env need be read at all, and once immediately before the .env is rewritten, whose
// answer is the one the rewrite acts on. Nothing is cached between the two — a snapshot
// reused across a write is the thing the second call exists to avoid.
func planProfileRemoval(name string) (profileRemoval, error) {
	if name == profiles.DefaultProfile {
		return profileRemoval{}, fmt.Errorf("cannot remove the %q profile", profiles.DefaultProfile)
	}
	c, err := profiles.Load()
	if err != nil {
		return profileRemoval{}, err
	}
	p, ok := c.Profiles[name]
	if !ok {
		return profileRemoval{}, fmt.Errorf("profile %q not found", name)
	}
	return profileRemoval{
		name:               name,
		baseURL:            p.BaseURL,
		deploymentSurvives: describedByAnotherProfile(c, name, p.BaseURL),
		ownKeys:            keysOnlyHeldBy(c, name),
	}, nil
}

// describedByAnotherProfile reports whether any profile other than name describes the
// deployment at baseURL — that is, whether that deployment outlives this removal.
//
// "Describes" is profiles.SameBaseURL, which is the same question knownDeployment asks
// of a project pin, and deliberately the same spelling of it: two notions of "this
// profile describes that deployment" would eventually disagree, and the disagreement
// would be silent. SameBaseURL is false for an empty base URL, so a profile for stock
// Blocks Network describes no deployment of its own and cannot speak for one — there,
// what the .env holds is decided by key identity, the only evidence available.
func describedByAnotherProfile(c *profiles.Contexts, name, baseURL string) bool {
	for profileName, p := range c.Profiles {
		if profileName == name {
			continue
		}
		if profiles.SameBaseURL(baseURL, p.BaseURL) {
			return true
		}
	}
	return false
}

// keysOnlyHeldBy lists the API keys the named profile cached and no other profile in the
// store also caches. The exclusion is what makes an identity match evidence of ownership
// rather than of use: two profiles can hold one key, and that key is still the credential
// for a target that still has a profile after this one is removed.
//
// Where those two profiles record the same base URL, describedByAnotherProfile already
// says so and says it about all three .env values. This is what remains for the profiles
// it cannot speak for: two profiles for stock Blocks Network record no deployment, so a
// key they share can only be recognised as shared by its bytes. Empty keys are not keys,
// and would match an unset .env value.
func keysOnlyHeldBy(c *profiles.Contexts, name string) []string {
	elsewhere := map[string]bool{}
	for profileName, p := range c.Profiles {
		if profileName == name {
			continue
		}
		for _, org := range p.Orgs {
			elsewhere[org.ApiKey] = true
		}
	}
	var keys []string
	for _, org := range c.Profiles[name].Orgs {
		if org.ApiKey != "" && !elsewhere[org.ApiKey] {
			keys = append(keys, org.ApiKey)
		}
	}
	return keys
}

// removeDeploymentFromProjectEnv drops everything the project .env holds for the
// deployment of the profile being removed, and reports which assignments it dropped so
// the caller can name them once the profile itself is gone. Left behind, those values
// keep sending work in this directory to a deployment there are no longer credentials
// for, while the profile list shows only the profile those commands are not using.
//
// They go as one unit because login writes them as one: BLOCKS_BACKEND_URL redirects a
// consumer's REST calls, BLOCKS_CDM_URL is where an agent runtime resolves that
// deployment's PubNub keysets and its own REST origin, and BLOCKS_API_KEY is the key
// minted at that deployment to spend there. Dropping only some of them forgets part of
// a target: a surviving CDM pin keeps a directly executed agent or trigger subscribed
// to the deployment the profile list no longer mentions, and a surviving key is sent —
// by the next command in this directory — either to Blocks Network or to whichever
// profile is now active.
//
// Targeting naming some other deployment is deliberate and none of this command's
// business, so each pin is dropped only on a deployment match — compared through
// profiles.SameBaseURL, the single definition of "same deployment", so a pin written
// `https://host/` and a profile recorded `https://host` are not mistaken for
// different places. The CDM value to compare comes from cdm.EndpointFor rather than
// being respelled here, so the comparison cannot drift from what login wrote. No
// .env, nothing pinned, or pins pointing elsewhere: nothing is changed and nothing is
// said.
//
// A .env that exists and cannot be read or rewritten fails the whole command, with the
// profile still in place — see runProfileRemove for why that is the safe direction. The
// failure names the profile that was not removed, because "could not clean .env" on its
// own leaves the user guessing which of the two halves happened.
//
// This deliberately differs from `blocks logout`, which removes only the key and keeps
// both pins: there the profile survives and a later `blocks login` is meant to return
// to it. `profile remove` forgets a deployment; `logout` forgets a credential.
func removeDeploymentFromProjectEnv(envPath string, removal profileRemoval) ([]string, error) {
	// The pre-flight's answer is allowed to end this early because doing so only ever
	// declines to delete anything: a deployment another profile still describes keeps
	// every value in the .env. Stopping here is also what keeps an unreadable .env from
	// failing a removal that was never going to touch it.
	if removal.deploymentSurvives {
		return nil, nil
	}
	targeting, err := readProjectEnvTargeting(envPath)
	if err != nil {
		return nil, fmt.Errorf("profile %q was not removed: could not tell whether %s still targets its deployment: %w",
			removal.name, displayEnvPath(envPath), err)
	}
	// The question is asked again here, of a store loaded after the .env has been read
	// and immediately before it is rewritten, because the answer decides whether any of
	// these values may be deleted and the pre-flight's copy of it is older than the
	// write it authorizes. A `blocks login` that adds a second name for the same
	// deployment in between makes the deployment survive this removal, and the .env then
	// still holds a backend pin, a CDM pin and a key that the surviving profile serves —
	// deleted, on the strength of a snapshot taken before that profile existed.
	//
	// Two processes racing cannot be made atomic across two files, so the goal is only
	// that the decision is as fresh as the write rather than that the race is closed.
	// A failure here is the store having changed under the command in a way the
	// pre-flight would have refused — another process removing the same profile first.
	// Nothing has been written yet, so it stops, and says which of the two halves
	// happened for the same reason every other failure on this path does.
	fresh, err := planProfileRemoval(removal.name)
	if err != nil {
		return nil, fmt.Errorf("profile %q was not removed: the profile store changed while it was being removed: %w",
			removal.name, err)
	}
	keys := envKeysBoundToDeployment(targeting, fresh)
	if len(keys) == 0 {
		return nil, nil
	}
	removed, err := auth.RemoveEnvKeys(envPath, keys...)
	if err != nil {
		return nil, fmt.Errorf("profile %q was not removed: %s still targets its deployment: %w",
			removal.name, displayEnvPath(envPath), err)
	}
	return removed, nil
}

// envKeysBoundToDeployment reports which of the project .env's assignments belong to the
// profile being removed: the pins naming its deployment, plus the credential that is only
// spendable there.
//
// Nothing belongs to the removal while another profile still describes the same
// deployment — an alias and its host, as a second `login` can produce. Removing one of
// those names forgets a name, not a deployment: the survivor still targets it, still
// holds credentials for it, and `blocks profile list` still shows it, so a .env pinned
// there is still exactly right and every value in it stays. The question is asked once,
// ahead of both routes below, because they have to agree — the key rule already excluded
// a key a surviving profile holds while the pin rule did not, so a .env pinned to an
// aliased deployment lost all three of its values to the removal of a spare name.
//
// With the deployment genuinely being forgotten, two independent things can tie the key
// to the removed profile, and either is enough:
//
//  1. The backend pin names its deployment. The .env is then saying where that key is
//     spent, which is the only thing a key's own bytes cannot say. This is also what
//     keeps the scope honest in the mixed case: a .env whose backend pin names a
//     deployment that still exists keeps its key, because that key is still the
//     credential for the target it sits beside.
//
//  2. The key is byte-identical to one the removed profile cached — and to no key any
//     surviving profile holds. There is then no pin to consult and none needed: the
//     store itself said this key was minted for this profile. Without this rule a
//     `blocks login --network --profile <name> --write-env` leaves its key behind
//     forever, because stock Network is deliberately left unpinned, so rule 1 has
//     nothing to match and every later command in this directory keeps authenticating
//     with a credential whose profile is gone.
//
// The limit of rule 2, stated plainly: it can only recognise a key the store still
// remembers. A key the user pasted in by hand, a key from a profile whose cache was
// cleared by `blocks logout`, or one rotated since it was written, is not recognised and
// is left alone — an unexplained key is somebody else's until proven otherwise, and
// deleting a working credential for another deployment is the worse mistake. It is the
// conservative direction of the same trade rule 1 makes.
//
// It performs no I/O of its own: it decides over a .env already read and a store
// snapshot the caller is responsible for having loaded late enough. Both halves of the
// decision — whether the deployment survives, and which keys only this profile
// holds — come from that one snapshot, so they cannot describe two different stores.
// See removeDeploymentFromProjectEnv for when the snapshot must be taken.
func envKeysBoundToDeployment(targeting projectEnvTargeting, removal profileRemoval) []string {
	if removal.deploymentSurvives {
		return nil
	}
	keys := targeting.pinsNamingDeployment(removal.baseURL)
	if slices.Contains(keys, blocksBackendURLEnv) {
		return append(keys, blocksAPIKeyEnv)
	}
	if targeting.apiKey != "" && slices.Contains(removal.ownKeys, targeting.apiKey) {
		return append(keys, blocksAPIKeyEnv)
	}
	return keys
}

// projectEnvTargeting is everything the project .env says about which deployment work
// in this directory reaches: the two pins login writes, and the key it minted there.
//
// Read in one pass so that the decision about the file and the rewrite that acts on it
// cannot be made against two different versions of it, and so an unreadable .env is one
// error rather than a value silently read as absent — a line this cannot see is a line
// the removal would not drop.
type projectEnvTargeting struct {
	backendPin string
	cdmPin     string
	apiKey     string
}

// readProjectEnvTargeting reads the three assignments through the same
// auth.EnvFileValue the rewrite uses, so a value this decides about is exactly a value
// the removal would delete.
func readProjectEnvTargeting(envPath string) (projectEnvTargeting, error) {
	var targeting projectEnvTargeting
	for _, field := range []struct {
		key  string
		dest *string
	}{
		{blocksBackendURLEnv, &targeting.backendPin},
		{cdm.URLEnv, &targeting.cdmPin},
		{blocksAPIKeyEnv, &targeting.apiKey},
	} {
		value, err := auth.EnvFileValue(envPath, field.key)
		if err != nil {
			return projectEnvTargeting{}, err
		}
		*field.dest = value
	}
	return targeting, nil
}

// pinsNamingDeployment reports which of the .env's deployment pins name the deployment
// at baseURL. The comparison is profiles.SameBaseURL — the single definition of "same
// deployment" — and the CDM value to compare against comes from cdm.EndpointFor rather
// than being respelled here, so neither can drift from what login wrote.
func (t projectEnvTargeting) pinsNamingDeployment(baseURL string) []string {
	var keys []string
	for _, pin := range []struct{ key, value, target string }{
		{blocksBackendURLEnv, t.backendPin, baseURL},
		{cdm.URLEnv, t.cdmPin, cdm.EndpointFor(baseURL)},
	} {
		if profiles.SameBaseURL(pin.value, pin.target) {
			keys = append(keys, pin.key)
		}
	}
	return keys
}

// listAnd joins names the way the sentence around them reads: "A", "A and B",
// "A, B and C".
func listAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
