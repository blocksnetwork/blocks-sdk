package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

// Backend strings are untrusted on the terminal: termsafe.Text for human output, encoding/json for --json.
var connectorCmd = &cobra.Command{
	Use:    "connector",
	Short:  "Manage PubNub Connectors (Blocks Enterprise)",
	Long:   "Create, list, edit and revoke PubNub Connectors, and manage the PubNub accounts and keysets behind them.\nWorks only on Blocks Enterprise deployments with PubNub Connectors turned on.",
	Hidden: true,
}

func init() {
	rootCmd.AddCommand(connectorCmd)
}

const connectorsOffMessage = "PubNub Connectors are not turned on for this deployment."

var errConnectorsOff = errors.New(connectorsOffMessage)

var errAccountsUnavailable = errors.New("connecting a PubNub account is not available on this deployment")

type sessionOpts struct {
	banner bool
	quiet  bool
	yes    bool
}

type connectorEnv struct {
	ctx         context.Context
	api         *connectors.Client
	out         io.Writer
	interactive bool
	wizard      bool
}

func withConnectorEnv(cmd *cobra.Command, o sessionOpts, run func(e *connectorEnv) error) error {
	if !clictx.Enterprise() {
		return errConnectorsOff
	}
	api, err := connectorAPIClient()
	if err != nil {
		return err
	}
	e := &connectorEnv{
		ctx:         cmd.Context(),
		api:         connectors.NewClient(api),
		out:         os.Stdout,
		interactive: interactiveSession() && !o.yes && !o.quiet,
	}
	if o.quiet {
		e.out = os.Stderr
	}
	if o.banner && !o.quiet {
		clictx.PrintBanner()
	}
	if err := e.preflight(); err != nil {
		return err
	}
	return run(e)
}

func connectorAPIClient() (*blocksapi.Client, error) {
	api, err := searchClient()
	if err != nil {
		return nil, err
	}
	if api.APIKey == "" {
		return nil, errors.New("not logged in: run `blocks login`")
	}
	return api, nil
}

func (e *connectorEnv) preflight() error {
	orgs, err := e.api.ListOrgs(e.ctx)
	if err != nil {
		return e.explain(err, "")
	}
	for _, o := range orgs {
		if o.PubnubConnectorsEnabled {
			return nil
		}
	}
	return errConnectorsOff
}

const permissionHint = "Run `blocks login --org <org that holds the permission>`."

var codeMessages = map[string]string{
	connectors.CodeExposureConsentRequired: "this keyset has no Access Manager and needs your consent. Pass --consent to confirm.",
	connectors.CodeExposureKindUnsupported: "this keyset cannot serve that kind. Pick another kind or another keyset.",
	connectors.CodeKeysetDisabled:          "the keyset is disabled. Pick another one; see `blocks connector keyset list`.",
	connectors.CodeKeysetNotFound:          "no such linked keyset. See `blocks connector keyset list`.",
	connectors.CodeExposureOrgMismatch:     "the Connector's org is not the org of your login. Run `blocks login --org <org>` for the right org.",
	connectors.CodeExposureOrgNotAllowed:   "Connectors cannot be created in this org. Run `blocks login --org <org>` for a regular org.",
	connectors.CodeExposureRevoked:         "that Connector is revoked and cannot be changed.",
	connectors.CodeExposureNotFound:        "no such Connector. See `blocks connector list`.",
	connectors.CodeConnectionNotFound:      "no such PubNub account connection. See `blocks connector account list`.",
	connectors.CodeConnectionDead:          "that PubNub account connection no longer works. Run `blocks connector account connect`.",
	connectors.CodeInsufficientRights:      "the connected PubNub account lacks the rights for that. Reconnect with an account that has them.",
	connectors.CodeAccountUnavailable:      "PubNub did not answer. Try again in a moment.",
	connectors.CodeAdminAPIRateLimited:     "PubNub is rate limiting requests. Try again in a minute.",
}

func (e *connectorEnv) explain(err error, agent string) error {
	var api *blocksapi.APIError
	if !errors.As(err, &api) {
		return err
	}
	switch {
	case connectors.IsNameTaken(err):
		return errNameTaken(agent)
	case connectors.IsFeatureOff(err):
		return errConnectorsOff
	case connectors.IsPermissionDenied(err):
		return fmt.Errorf("you do not hold the PubNub Connectors permission in %s. %s", orgLabel(), permissionHint)
	case api.StatusCode == 401:
		return errors.New("the deployment rejected your credentials: run `blocks login`")
	}
	if msg, ok := codeMessages[api.Code]; ok {
		return errors.New(msg)
	}
	return fmt.Errorf("%s (HTTP %d)", termsafe.Text(api.Message), api.StatusCode)
}

func (e *connectorEnv) explainAccount(err error) error {
	if connectors.IsFeatureOff(err) {
		return errAccountsUnavailable
	}
	return e.explain(err, "")
}

func errNameTaken(name string) error {
	return fmt.Errorf("the agent name %s is already taken. See `blocks connector list`, or pick another name", shownName(name))
}

func orgLabel() string {
	if org := clictx.Org(); org != "" {
		return termsafe.Text(org)
	}
	return "this org"
}

func (e *connectorEnv) printf(format string, a ...any) { fmt.Fprintf(e.out, format, a...) }

func (e *connectorEnv) println(a ...any) { fmt.Fprintln(e.out, a...) }

func printConnectorJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newConnectorTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

func tableRow(tw *tabwriter.Writer, cols ...string) {
	safe := make([]string, len(cols))
	for i, c := range cols {
		safe[i] = termsafe.Text(c)
	}
	fmt.Fprintln(tw, strings.Join(safe, "\t"))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (e *connectorEnv) promptLine(label string) (string, error) {
	fmt.Fprint(e.out, label+": ")
	line, ok, _ := readStdinLine()
	if !ok {
		return "", fmt.Errorf("no answer given for %q", label)
	}
	return line, nil
}

func (e *connectorEnv) confirm(prompt string) bool {
	answer, answered := wizard.AskYesNo(readStdinAnswer, prompt, false, "Answer y to continue, n to stop.")
	return answered && answer
}

func (e *connectorEnv) selectOne(prompt string, options []string) (int, error) {
	return wizard.InteractiveSelect(prompt, options, 0, "Use the arrow keys, then press enter.")
}

func (e *connectorEnv) requireConfirmation(yes bool, prompt, noInteractiveHint string) (bool, error) {
	if yes {
		return true, nil
	}
	if !interactiveSession() {
		return false, errors.New(noInteractiveHint)
	}
	return e.confirm(prompt), nil
}
