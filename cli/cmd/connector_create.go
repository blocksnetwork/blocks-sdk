package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

type connectorCreateFlags struct {
	connectorShape
	keyset       string
	pubnubKeyset string
	account      string
	label        string
	kind         string
	consent      bool
	yes          bool
	json         bool
}

var connectorCreateOpts connectorCreateFlags

func init() {
	connectorCmd.AddCommand(connectorCreateCmd)
	f := connectorCreateCmd.Flags()
	o := &connectorCreateOpts
	f.StringVar(&o.keyset, "keyset", "", "Use a keyset already linked to this deployment, by id or label")
	f.StringVar(&o.pubnubKeyset, "pubnub-keyset", "", "Link this PubNub keyset id from the connected PubNub account first (reused if already linked)")
	f.StringVar(&o.account, "account", "", "PubNub account connection to use, by id or name")
	f.StringVar(&o.label, "label", "", "Label when linking a keyset (default \"<app> / <keyset>\")")
	f.StringVar(&o.kind, "kind", "", "Kind: subscribe, history, presence or publish")
	f.BoolVar(&o.consent, "consent", false, "Confirm a keyset without Access Manager, where callers receive its subscribe key")
	f.BoolVar(&o.yes, "yes", false, "Never prompt: use the flags and defaults")
	f.BoolVar(&o.json, "json", false, "Print the created Connector as JSON (never prompts)")
	addShapeFlags(connectorCreateCmd, &o.connectorShape)
	connectorCreateCmd.MarkFlagsMutuallyExclusive("keyset", "pubnub-keyset")
}

var connectorCreateCmd = &cobra.Command{
	Use:   "create [agent-name]",
	Short: "Create a PubNub Connector for an agent",
	Long: "Create a PubNub Connector: an agent that hands callers access to a PubNub keyset.\n\n" +
		"The keyset is found in this order: --keyset (already linked), --pubnub-keyset (linked from the\n" +
		"connected PubNub account, or reused if already linked), then a picker. The browser opens only when a\n" +
		"keyset still has to be linked and no PubNub account is connected in your org.",
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.MaximumNArgs(1)(cmd, args); err != nil || len(args) == 0 {
			return err
		}
		return wizard.ValidateAgentName(args[0])
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		o := connectorCreateOpts
		o.captureChanged(cmd)
		return withConnectorEnv(cmd, sessionOpts{banner: true, quiet: o.json, yes: o.yes}, func(e *connectorEnv) error {
			return e.createConnector(args, o)
		})
	},
}

func (e *connectorEnv) createConnector(args []string, o connectorCreateFlags) error {
	orgID := clictx.OrgID()
	if orgID == "" {
		return errNoOrgID
	}
	name, err := e.requireText(firstArg(args), "Agent name", "the agent name argument")
	if err != nil {
		return err
	}
	if err := wizard.ValidateAgentName(name); err != nil {
		return err
	}
	if err := e.ensureNameFree(name); err != nil {
		return err
	}
	e.wizard = e.interactive && o.kind == ""
	if err := e.checkBeforeLinking(o); err != nil {
		return err
	}
	ks, err := e.keysetForCreate(o)
	if err != nil {
		return err
	}
	req, err := e.createRequest(name, orgID, ks, o)
	if err != nil {
		return err
	}
	exp, err := e.api.CreateExposure(e.ctx, req)
	if err != nil {
		return e.explain(err, name)
	}
	if o.json {
		return printConnectorJSON(exp)
	}
	e.printCreated(exp)
	return nil
}

func (e *connectorEnv) checkBeforeLinking(o connectorCreateFlags) error {
	switch {
	case o.kind == "" && !e.interactive:
		return errors.New(kindFlagHint)
	case o.kind == "":
		_, err := o.connectorShape.config(nil)
		return err
	}
	if _, known := kindNames[o.kind]; !known {
		return fmt.Errorf("unknown kind %q: %s", termsafe.Text(o.kind), kindFlagHint)
	}
	return e.checkBeforeKeyset(o.kind, o.connectorShape)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func (e *connectorEnv) ensureNameFree(name string) error {
	exposures, err := e.api.ListExposures(e.ctx)
	if err != nil {
		return e.explain(err, name)
	}
	if _, err := connectors.ResolveExposureByAgentName(exposures, name); err == nil || errors.Is(err, connectors.ErrAmbiguous) {
		return errNameTaken(name)
	}
	return nil
}

func (e *connectorEnv) keysetForCreate(o connectorCreateFlags) (connectors.LinkedKeyset, error) {
	switch {
	case o.keyset != "":
		return e.resolveLinkedKeyset(o.keyset)
	case o.pubnubKeyset != "":
		return e.linkPubnubKeyset(o.pubnubKeyset, o.account, o.label)
	case !e.interactive:
		return connectors.LinkedKeyset{}, errors.New("a keyset is required: pass --keyset (a linked keyset) or --pubnub-keyset (a PubNub keyset id)")
	}
	return e.pickKeyset(o)
}

const connectAccountOption = "Connect a PubNub account and link another keyset"

func (e *connectorEnv) pickKeyset(o connectorCreateFlags) (connectors.LinkedKeyset, error) {
	choice, err := e.chooseConnection(o.account)
	if errors.Is(err, errAccountsUnavailable) {
		return e.pickLinkedOnly(errAccountsUnavailable)
	}
	if err != nil {
		return connectors.LinkedKeyset{}, err
	}
	if choice.Use != nil {
		e.announceConnection(*choice.Use)
		return e.pickFromAccount(*choice.Use, o.label)
	}
	if choice.OwnDead != nil {
		return e.reconnectOrPickLinked(*choice.OwnDead, o)
	}
	k, picked, err := e.pickLinked(true)
	if err != nil || picked {
		return k, err
	}
	return e.connectAndPick(o.label)
}

func (e *connectorEnv) pickLinkedOnly(reason error) (connectors.LinkedKeyset, error) {
	k, picked, err := e.pickLinked(false)
	if err != nil || picked {
		return k, err
	}
	return k, fmt.Errorf("%w; pass --keyset with a keyset already linked", reason)
}

func (e *connectorEnv) connectAndPick(label string) (connectors.LinkedKeyset, error) {
	conn, err := e.connectInBrowser()
	if err != nil {
		return connectors.LinkedKeyset{}, err
	}
	return e.pickFromAccount(conn, label)
}

func (e *connectorEnv) reconnectOrPickLinked(dead connectors.AccountConnection, o connectorCreateFlags) (connectors.LinkedKeyset, error) {
	if e.confirmReconnect(dead) {
		return e.connectAndPick(o.label)
	}
	return e.pickLinkedOnly(errors.New("no usable PubNub account connection. Run `blocks connector account connect` to reconnect"))
}

func (e *connectorEnv) pickLinked(offerConnect bool) (k connectors.LinkedKeyset, picked bool, err error) {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return k, false, e.explain(err, "")
	}
	keysets = connectors.VisibleKeysets(keysets, false)
	if len(keysets) == 0 {
		return k, false, nil
	}
	options := make([]string, 0, len(keysets)+1)
	for _, ks := range keysets {
		options = append(options, fmt.Sprintf("%s   %s", termsafe.Text(ks.Label), termsafe.Text(ks.SubscribeKey)))
	}
	if offerConnect {
		options = append(options, connectAccountOption)
	}
	i, err := e.selectOne("Linked keyset", options)
	if err != nil || i == len(keysets) {
		return k, false, err
	}
	return keysets[i], true, nil
}

func (e *connectorEnv) pickFromAccount(conn connectors.AccountConnection, label string) (connectors.LinkedKeyset, error) {
	rows, err := e.pickerRowsOf(conn)
	if err != nil {
		return connectors.LinkedKeyset{}, err
	}
	if len(rows) == 0 {
		return connectors.LinkedKeyset{}, fmt.Errorf("the PubNub account %q has no keysets", termsafe.Text(conn.Name))
	}
	options := make([]string, len(rows))
	for i, r := range rows {
		options[i] = r.option()
	}
	i, err := e.selectOne("PubNub keyset", options)
	if err != nil {
		return connectors.LinkedKeyset{}, err
	}
	return e.linkRow(conn, rows[i], label)
}

func (e *connectorEnv) createRequest(name, orgID string, ks connectors.LinkedKeyset, o connectorCreateFlags) (connectors.CreateExposureRequest, error) {
	req := connectors.CreateExposureRequest{OrgID: orgID, LinkedKeysetID: ks.ID, AgentName: name}
	kind, err := e.chooseKind(ks, o.kind)
	if err != nil {
		return req, err
	}
	shape := o.connectorShape
	if err := shape.checkKind(kind); err != nil {
		return req, err
	}
	if err := e.fillShape(kind, &shape); err != nil {
		return req, err
	}
	cfg, err := shape.config(nil)
	if err != nil {
		return req, err
	}
	consent, err := e.consentFor(ks, kind, o.consent)
	if err != nil {
		return req, err
	}
	req.Kind, req.DisplayName, req.Description, req.ChannelTemplate = kind, shape.displayName, shape.description, shape.channelTemplate
	if shape.raw() {
		req.RawSelector = shape.rawSelector()
	}
	if len(cfg) > 0 {
		req.Config = cfg
	}
	req.Consent = consent
	return req, nil
}

var kindNames = map[string]string{
	connectors.KindSubscribe: "Subscribe",
	connectors.KindHistory:   "History",
	connectors.KindPresence:  "Presence",
	connectors.KindPublish:   "Publish",
}

const kindFlagHint = "--kind must be subscribe, history, presence or publish"

func (e *connectorEnv) chooseKind(ks connectors.LinkedKeyset, flag string) (string, error) {
	if flag != "" {
		if err := connectors.ValidateKind(ks.Features, flag); err != nil {
			return "", fmt.Errorf("%s. Pick another kind or keyset", termsafe.Text(err.Error()))
		}
		return flag, nil
	}
	if !e.interactive {
		return "", errors.New(kindFlagHint)
	}
	opts := connectors.KindOptions(ks.Features)
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = kindNames[o.Kind]
		if !o.Available {
			labels[i] += " (" + o.Reason + ")"
		}
	}
	for {
		i, err := e.selectOne("Kind", labels)
		if err != nil {
			return "", err
		}
		if opts[i].Available {
			return opts[i].Kind, nil
		}
		e.printf("%s.\n", opts[i].Reason)
	}
}

func (e *connectorEnv) consentFor(ks connectors.LinkedKeyset, kind string, flag bool) (bool, error) {
	e.println(connectors.CallerAccessNotice(kind))
	if ks.Mode != connectors.ModeConsent {
		return false, nil
	}
	e.printf("%q has no Access Manager: %s\n", termsafe.Text(ks.Label), connectors.ConsentNotice)
	switch {
	case flag:
		return true, nil
	case !e.interactive:
		return false, errors.New("pass --consent to confirm")
	case e.confirm("Create this Connector anyway?"):
		return true, nil
	}
	return false, errors.New("nothing was created: consent was not given")
}

func (e *connectorEnv) printCreated(exp connectors.Exposure) {
	name := termsafe.Text(exp.AgentName)
	line := fmt.Sprintf("Created Connector %s (%s on %s", name, kindNames[exp.Kind], termsafe.Text(addressing(exp)))
	if exp.AllValues.Eligible {
		line += "; all values: " + termsafe.Text(exp.AllValues.Pattern)
	}
	e.println(line + ")")
	if base, err := resolveDashboardURL(); err == nil {
		if u, err := dashboardURL(base, exp.AgentName); err == nil {
			e.println("Open: " + termsafe.Text(u))
		}
	}
}

func addressing(exp connectors.Exposure) string {
	if exp.ChannelTemplate != nil {
		return *exp.ChannelTemplate
	}
	if exp.RawSelector == nil {
		return ""
	}
	var parts []string
	if len(exp.RawSelector.Channels) > 0 {
		parts = append(parts, "channels "+strings.Join(exp.RawSelector.Channels, ", "))
	}
	if len(exp.RawSelector.Patterns) > 0 {
		parts = append(parts, "patterns "+strings.Join(exp.RawSelector.Patterns, ", "))
	}
	return "raw " + strings.Join(parts, "; ")
}
