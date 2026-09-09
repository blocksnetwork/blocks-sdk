package cmd

import (
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

// backendOverrideNote renders the note that tells a user reading profile-shaped
// output that an ambient BLOCKS_BACKEND_URL, not the profile in front of them,
// decides where commands go. It returns "" whenever the active profile really is
// the target, so the output people read constantly is unchanged in the common
// case.
//
// The determination belongs to clictx and is taken from it verbatim:
// ProfileIsTarget() already answers "does the active profile describe the backend
// this invocation will call", and ChosenBackendURL() already names that backend.
// Recomputing the precedence here is exactly the duplication clictx exists to
// prevent. Those two also pin the note to a real override, because the resolver
// reports a chosen backend the profile does not describe only when an ambient
// backend URL supplied it.
//
// An override naming the very deployment the profile records — what
// `blocks login --write-env` leaves behind in its own project directory — is not
// reported: nothing is being redirected there, and ProfileIsTarget() is true.
//
// The source is named only when it is known. loadEnvFile records which variables
// it set from a project .env; a variable absent from that record was exported by
// the shell, which the CLI cannot observe as such, so the note says nothing about
// where the value came from rather than asserting a source it cannot verify.
//
// Both of the values the note interpolates originate outside the program — the
// backend URL is whatever an environment variable held, the path is whatever
// directory the command was run from — so both are rendered through termsafe. The
// note's whole job is to contradict the profile-shaped output printed beside it, and
// a value able to erase that line would defeat it. The variable name is a compiled-in
// constant and is left alone.
func backendOverrideNote() string {
	if clictx.ProfileIsTarget() {
		return ""
	}
	target := clictx.ChosenBackendURL()
	if target == "" {
		return ""
	}
	where := ""
	if src := envFileSuppliedCLIVar(blocksBackendURLEnv); src != "" {
		where = " in " + termsafe.Text(src)
	}
	return blocksBackendURLEnv + where + " overrides this → " + termsafe.Text(target)
}

// backendOverrideNoteLine is the note as a ready-to-print line indented under
// whatever it qualifies, or "" when no override is in force. Returning the empty
// string rather than requiring a guard at each call site is deliberate: printing
// it unconditionally cannot accidentally add a blank line to the common output.
//
// It is worded and placed as a note, not a warning: an ambient backend URL is the
// supported way to point a headless run at a deployment, so it goes to stdout
// beside the output it explains and carries no warning vocabulary.
func backendOverrideNoteLine(indent string) string {
	n := backendOverrideNote()
	if n == "" {
		return ""
	}
	return indent + "Note: " + n + "\n"
}
