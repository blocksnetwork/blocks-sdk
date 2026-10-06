package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

// displayHomePath shortens a path under the home directory to ~/…, the form a user
// recognises and can paste into a shell.
func displayHomePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}

// profileStorePath is the absolute path of the file `blocks login` stores profile keys
// in, for machine-readable output: unlike the ~/… display form, filesystem APIs and
// quoted shell variables can use it as-is.
func profileStorePath() (string, error) {
	p, err := profiles.ContextsPathFunc()
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

// profileStoreDisplayPath names the file `blocks login` stores profile keys in.
func profileStoreDisplayPath() string {
	p, err := profiles.ContextsPathFunc()
	if err != nil {
		return "contexts.json"
	}
	return displayHomePath(p)
}

func legacyStoreDisplayPath() string {
	p, err := auth.CredentialPathFunc()
	if err != nil {
		return "credentials.json"
	}
	return displayHomePath(p)
}

// credentialSourceDescription says where the key this invocation sends came from,
// or "" when it sends none.
func credentialSourceDescription(c clictx.Credential) string {
	if c.Key == "" {
		return ""
	}
	switch c.Source {
	case clictx.SourceFlag:
		return "--api-key"
	case clictx.SourceStdin:
		return "--api-key-stdin"
	case clictx.SourceEnv:
		// envFileSuppliedCLIVar, not envFileSource: provenance is filed under the canonical
		// name, so on Unix a file assigning blocks_api_key would otherwise claim credit for
		// a BLOCKS_API_KEY the shell exported — a different variable there.
		if file := envFileSuppliedCLIVar(blocksAPIKeyEnv); file != "" {
			return blocksAPIKeyEnv + " in " + file
		}
		return blocksAPIKeyEnv + " in the environment"
	case clictx.SourceProfile:
		return fmt.Sprintf("CLI profile %q (%s)", termsafe.Text(clictx.Profile()), profileStoreDisplayPath())
	case clictx.SourceStore:
		return "the legacy credential store (" + legacyStoreDisplayPath() + ")"
	}
	return ""
}

// noteStoredCredential tells the user that a command is authenticating with the key
// `blocks login` stored, because neither the project .env nor the environment names
// one. Reusing the login is the point; saying so is what keeps it from being a
// surprise when a project later needs its own key.
func noteStoredCredential(w io.Writer) {
	c := clictx.EffectiveCredential()
	if c.Source != clictx.SourceProfile && c.Source != clictx.SourceStore {
		return
	}
	if desc := credentialSourceDescription(c); desc != "" {
		fmt.Fprintf(w, "Using the API key from %s — no %s is set in .env.\n", desc, blocksAPIKeyEnv)
		fmt.Fprintf(w, "  To use a different key for this project, add %s=<key> to .env.\n", blocksAPIKeyEnv)
	}
}
