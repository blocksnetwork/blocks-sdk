package cmd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// The profile store is the CLI's record of which deployments the user deliberately
// logged in to, and it is the only evidence the backend-pin decline has. Reading it can
// fail — wrong permissions after a `sudo` run, a truncated write, a directory in its
// place, JSON a partial write left unterminated — and a failure is not the same fact as
// "no deployments are trusted", even though both leave the decline with an empty list to
// match against.
//
// Collapsing the two is dangerous in a specific way. An ordinary project pin, the kind
// `blocks login --write-env` leaves in its own directory, looks foreign the moment the
// store cannot be read; it is dropped; and resolution falls through to the default
// deployment while the invocation still carries the API key that same project file
// supplied, which was minted somewhere else entirely. So an unreadable store has to stop
// the invocation, and an absent one — a user who has simply never logged in — must not.
//
// These cases drive the real pre-run hook rather than the helper alone, because the
// property under test is that the check happens before anything resolves a target: a
// helper that returns the right error but runs after the decline would fix nothing.

// unreadableProfileStore points the store at a path holding the given bytes, which the
// caller writes to be unparseable, and returns that path. Nothing stubs
// profiles.Load: the question is what the production load does with a broken file.
func unreadableProfileStore(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write contexts.json: %v", err)
	}
	return pointProfileStoreAt(t, path)
}

// pointProfileStoreAt makes path the store location for the duration of the test.
func pointProfileStoreAt(t *testing.T, path string) string {
	t.Helper()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })
	return path
}

// runStartupHook drives the real PersistentPreRunE and returns what it reported, so a
// failure that must stop the invocation is observed as the error cobra would surface
// rather than through a seam standing in for os.Exit.
func runStartupHook(t *testing.T) error {
	t.Helper()
	return rootCmd.PersistentPreRunE(unregisterCmd, nil)
}

// pinnedProjectEnvDir puts the test in a project directory whose .env pins a backend and
// carries a key minted for it — the shape `blocks login --write-env` leaves behind, and
// the shape whose mishandling this file is about.
func pinnedProjectEnvDir(t *testing.T, backendURL string) {
	t.Helper()
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		blocksBackendURLEnv+"="+backendURL+"\n"+blocksAPIKeyEnv+"=bk_minted_for_the_pin\n"))
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)
}

// The reproduction: a store that exists and cannot be parsed must stop the invocation
// naming the file, not quietly retarget the request.
func TestAnUnreadableProfileStoreStopsTheInvocation(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	isolateAmbientState(t)
	clictx.Reset()

	// A partial write: the file is there, the JSON is not finished.
	path := unreadableProfileStore(t, `{"schema_version":3,"profiles":{"acme":{"base_url":`)
	const pinned = "https://pinned.example.test"
	pinnedProjectEnvDir(t, pinned)
	err := runStartupHook(t)

	if err == nil {
		t.Fatal("a store that exists and cannot be read was treated as a store that trusts nothing")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the failure must name the file to look at, got: %v", err)
	}
	// The two halves of "rather than resolving to the default deployment": the pin is
	// still there, so nothing decided it was foreign on the strength of a store it could
	// not read, and no target was latched, so no credential tier was read against one.
	if got := os.Getenv(blocksBackendURLEnv); got != pinned {
		t.Errorf("%s = %q, want the pin left alone — a store that cannot be read is no evidence that it is foreign", blocksBackendURLEnv, got)
	}
	if target := clictx.ChosenBackendURL(); target != "" {
		t.Errorf("a target was resolved after the store failed to load: %q", target)
	}
}

// A store whose file is a directory fails differently — the read itself errors rather
// than the parse — and must land in the same place. It is the case a half-finished
// install or a mis-aimed `mkdir -p` produces.
func TestAProfileStorePathThatIsNotAFileStopsTheInvocation(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	isolateAmbientState(t)
	clictx.Reset()

	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	pointProfileStoreAt(t, path)
	pinnedProjectEnvDir(t, "https://pinned.example.test")
	err := runStartupHook(t)

	if err == nil {
		t.Fatal("a store path that is not a readable file was treated as a store that trusts nothing")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the failure must name the file to look at, got: %v", err)
	}
}

// The other side of the distinction, and the one that must stay silent: a user who has
// never logged in has no contexts.json, nothing is wrong, and a pin naming a deployment
// no profile describes is still declined exactly as before.
func TestAProfileStoreThatWasNeverWrittenIsStillASilentEmpty(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	isolateAmbientState(t)
	clictx.Reset()

	pointProfileStoreAt(t, filepath.Join(t.TempDir(), "contexts.json"))
	pinnedProjectEnvDir(t, "https://pinned.example.test")
	err := runStartupHook(t)

	if err != nil {
		t.Fatalf("a store that was never written aborted the invocation: %v", err)
	}
	if value, ok := os.LookupEnv(blocksBackendURLEnv); ok {
		t.Errorf("%s = %q, want a pin no profile vouches for still declined", blocksBackendURLEnv, value)
	}
}

// loadProfileStore is where the distinction lives, so its two answers are asserted
// directly too: absence is a nil store and no error, and a path that cannot be produced
// at all still fails rather than reporting an empty store.
func TestLoadProfileStoreSeparatesAbsenceFromFailure(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		pointProfileStoreAt(t, filepath.Join(t.TempDir(), "contexts.json"))
		store, err := loadProfileStore()
		if err != nil {
			t.Fatalf("loadProfileStore() = %v, want no error for a store that was never written", err)
		}
		if knownDeployment(store, "https://pinned.example.test") {
			t.Error("an absent store vouched for a deployment")
		}
	})

	t.Run("no path", func(t *testing.T) {
		orig := profiles.ContextsPathFunc
		profiles.ContextsPathFunc = func() (string, error) { return "", errors.New("no home directory") }
		t.Cleanup(func() { profiles.ContextsPathFunc = orig })

		if _, err := loadProfileStore(); err == nil {
			t.Fatal("loadProfileStore() = nil, want a failure when the store location cannot be derived")
		} else if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a store whose location is unknown was reported as absent: %v", err)
		}
	})
}
