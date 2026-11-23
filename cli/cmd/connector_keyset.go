package cmd

import (
	"errors"
	"fmt"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

var (
	connectorKeysetAccount   string
	connectorKeysetLabel     string
	connectorKeysetJSON      bool
	connectorKeysetAll       bool
	connectorKeysetDisableOK bool
)

func init() {
	connectorCmd.AddCommand(connectorKeysetCmd)
	connectorKeysetCmd.AddCommand(connectorKeysetAvailableCmd, connectorKeysetLinkCmd, connectorKeysetListCmd,
		connectorKeysetValidateCmd, connectorKeysetDisableCmd)
	connectorKeysetAvailableCmd.Flags().StringVar(&connectorKeysetAccount, "account", "", "PubNub account connection to use, by id or name")
	connectorKeysetAvailableCmd.Flags().BoolVar(&connectorKeysetJSON, "json", false, "Print JSON")
	connectorKeysetLinkCmd.Flags().StringVar(&connectorKeysetAccount, "account", "", "PubNub account connection to use, by id or name")
	connectorKeysetLinkCmd.Flags().StringVar(&connectorKeysetLabel, "label", "", "Label for the linked keyset (default \"<app> / <keyset>\")")
	connectorKeysetListCmd.Flags().BoolVar(&connectorKeysetAll, "all", false, "Include disabled keysets")
	connectorKeysetListCmd.Flags().BoolVar(&connectorKeysetJSON, "json", false, "Print JSON")
	connectorKeysetDisableCmd.Flags().BoolVar(&connectorKeysetDisableOK, "yes", false, "Skip the confirmation prompt")
}

var connectorKeysetCmd = &cobra.Command{
	Use:   "keyset",
	Short: "Manage the PubNub keysets linked to this deployment",
}

var connectorKeysetAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List the keysets of a connected PubNub account",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{quiet: connectorKeysetJSON}, func(e *connectorEnv) error {
			return e.availableKeysets(connectorKeysetAccount, connectorKeysetJSON)
		})
	},
}

var connectorKeysetLinkCmd = &cobra.Command{
	Use:   "link <pubnub-keyset-id>",
	Short: "Link a keyset of a connected PubNub account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{banner: true}, func(e *connectorEnv) error {
			_, err := e.linkPubnubKeyset(args[0], connectorKeysetAccount, connectorKeysetLabel)
			return err
		})
	},
}

var connectorKeysetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the linked keysets",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{quiet: connectorKeysetJSON}, func(e *connectorEnv) error {
			return e.listKeysets(connectorKeysetAll, connectorKeysetJSON)
		})
	},
}

var connectorKeysetValidateCmd = &cobra.Command{
	Use:   "validate <id|label>",
	Short: "Check that a linked keyset still works",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{banner: true}, func(e *connectorEnv) error {
			return e.validateKeyset(args[0])
		})
	},
}

var connectorKeysetDisableCmd = &cobra.Command{
	Use:   "disable <id|label>",
	Short: "Disable a linked keyset and revoke every Connector on it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{banner: true}, func(e *connectorEnv) error {
			return e.disableKeyset(args[0], connectorKeysetDisableOK)
		})
	},
}

func (e *connectorEnv) availableKeysets(accountRef string, asJSON bool) error {
	choice, err := e.chooseConnection(accountRef)
	if err != nil {
		return err
	}
	if choice.Use == nil {
		return e.needBrowserError(choice, false)
	}
	e.announceConnection(*choice.Use)
	apps, err := e.api.ListPickerApps(e.ctx, choice.Use.ID)
	if err != nil {
		return e.explainAccount(err)
	}
	if asJSON {
		return printConnectorJSON(apps)
	}
	tw := newConnectorTable(e.out)
	tableRow(tw, "APP", "KEYSET", "PUBNUB KEYSET ID", "SUBSCRIBE KEY", "LINKED")
	for _, row := range pickerRows(apps) {
		tableRow(tw, row.appName(), row.keysetName(), row.keyset.ID, row.keyset.SubscribeKey, yesNo(row.keyset.LinkedKeysetID != nil))
	}
	return tw.Flush()
}

func (e *connectorEnv) listKeysets(all, asJSON bool) error {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return e.explain(err, "")
	}
	keysets = connectors.VisibleKeysets(keysets, all)
	if asJSON {
		return printConnectorJSON(keysets)
	}
	if len(keysets) == 0 {
		e.println("No keysets are linked. Run `blocks connector keyset link <pubnub-keyset-id>`.")
		return nil
	}
	tw := newConnectorTable(e.out)
	tableRow(tw, "ID", "LABEL", "SUBSCRIBE KEY", "MODE", "SOURCE", "STATUS")
	for _, k := range keysets {
		tableRow(tw, k.ID, k.Label, k.SubscribeKey, k.Mode, k.Source, k.Status)
	}
	return tw.Flush()
}

func (e *connectorEnv) validateKeyset(ref string) error {
	k, err := e.resolveLinkedKeyset(ref)
	if err != nil {
		return err
	}
	res, err := e.api.ValidateKeyset(e.ctx, k.ID)
	if err != nil {
		return e.explain(err, "")
	}
	e.printf("Keyset %q works.\n", termsafe.Text(res.Label))
	if !res.ConfigChecked && res.ConfigSkippedReason != "" {
		e.printf("Its PubNub configuration was not checked: %s\n", termsafe.Text(res.ConfigSkippedReason))
	}
	return nil
}

func (e *connectorEnv) disableKeyset(ref string, yes bool) error {
	k, err := e.resolveKeyset(ref, true)
	if err != nil {
		return err
	}
	if k.Status == connectors.KeysetStatusDisabled {
		return e.retryDisable(k)
	}
	prompt := fmt.Sprintf("Disable keyset %q? This revokes every Connector on it, in every org.", termsafe.Text(k.Label))
	ok, err := e.requireConfirmation(yes, prompt, fmt.Sprintf("refusing to disable %q without confirmation: pass --yes. Disabling revokes every Connector on the keyset, in every org", termsafe.Text(k.Label)))
	if err != nil {
		return err
	}
	if !ok {
		e.println("Cancelled: nothing was disabled.")
		return nil
	}
	if _, err := e.api.DisableKeyset(e.ctx, k.ID); err != nil {
		return e.explain(err, "")
	}
	e.printf("Disabled keyset %q and revoked every Connector on it.\n", termsafe.Text(k.Label))
	return nil
}

func (e *connectorEnv) retryDisable(k connectors.LinkedKeyset) error {
	if _, err := e.api.DisableKeyset(e.ctx, k.ID); err != nil {
		return e.explain(err, "")
	}
	e.printf("Keyset %q was already disabled. Retried revoking the PubNub tokens of its Connectors.\n", termsafe.Text(k.Label))
	return nil
}

func (e *connectorEnv) resolveLinkedKeyset(ref string) (connectors.LinkedKeyset, error) {
	return e.resolveKeyset(ref, false)
}

func (e *connectorEnv) resolveKeyset(ref string, allowDisabled bool) (connectors.LinkedKeyset, error) {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return connectors.LinkedKeyset{}, e.explain(err, "")
	}
	k, err := connectors.ResolveKeyset(keysets, ref)
	switch {
	case errors.Is(err, connectors.ErrKeysetDisabled) && allowDisabled:
		return k, nil
	case errors.Is(err, connectors.ErrKeysetDisabled):
		return k, fmt.Errorf("keyset %q is disabled. See `blocks connector keyset list --all`", termsafe.Text(ref))
	case errors.Is(err, connectors.ErrNotFound):
		return k, fmt.Errorf("no linked keyset %q. See `blocks connector keyset list`", termsafe.Text(ref))
	case err != nil:
		return k, fmt.Errorf("%s. Pass the id instead", termsafe.Text(err.Error()))
	}
	return k, nil
}

type pickerRow struct {
	app    connectors.PickerApp
	keyset connectors.PickerKeyset
}

func (r pickerRow) appName() string { return nameOrID(r.app.Name, r.app.ID) }

func (r pickerRow) keysetName() string { return nameOrID(r.keyset.Name, r.keyset.ID) }

func nameOrID(name *string, id string) string {
	if name == nil || *name == "" {
		return id
	}
	return *name
}

func pickerRows(apps []connectors.PickerApp) []pickerRow {
	var rows []pickerRow
	for _, a := range apps {
		for _, k := range a.Keysets {
			rows = append(rows, pickerRow{app: a, keyset: k})
		}
	}
	return rows
}

func (r pickerRow) option() string {
	state := "(not linked)"
	if r.keyset.LinkedKeysetID != nil {
		state = "(linked)"
	}
	return fmt.Sprintf("%s / %s   %s   %s", termsafe.Text(r.appName()), termsafe.Text(r.keysetName()), termsafe.Text(r.keyset.SubscribeKey), state)
}

func (e *connectorEnv) linkPubnubKeyset(pubnubKeysetID, accountRef, label string) (connectors.LinkedKeyset, error) {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return connectors.LinkedKeyset{}, e.explain(err, "")
	}
	if k, ok := connectors.FindLinkedByPubnubKeysetID(keysets, pubnubKeysetID); ok {
		e.reportAlreadyLinked(k)
		return k, nil
	}
	conn, err := e.connectionForLinking(accountRef)
	if err != nil {
		return connectors.LinkedKeyset{}, err
	}
	rows, err := e.pickerRowsOf(conn)
	if err != nil {
		return connectors.LinkedKeyset{}, err
	}
	for _, r := range rows {
		if r.keyset.ID == pubnubKeysetID {
			return e.linkRow(conn, r, label)
		}
	}
	return connectors.LinkedKeyset{}, fmt.Errorf("no PubNub keyset %q in the account %q. See `blocks connector keyset available`",
		termsafe.Text(pubnubKeysetID), termsafe.Text(conn.Name))
}

func (e *connectorEnv) connectionForLinking(accountRef string) (connectors.AccountConnection, error) {
	choice, err := e.chooseConnection(accountRef)
	if err != nil {
		return connectors.AccountConnection{}, err
	}
	if choice.Use != nil {
		e.announceConnection(*choice.Use)
		return *choice.Use, nil
	}
	if !e.interactive {
		return connectors.AccountConnection{}, e.needBrowserError(choice, true)
	}
	if choice.OwnDead != nil && !e.confirmReconnect(*choice.OwnDead) {
		return connectors.AccountConnection{}, errors.New("no usable PubNub account connection. Run `blocks connector account connect` to reconnect")
	}
	return e.connectInBrowser()
}

func (e *connectorEnv) pickerRowsOf(conn connectors.AccountConnection) ([]pickerRow, error) {
	apps, err := e.api.ListPickerApps(e.ctx, conn.ID)
	if err != nil {
		return nil, e.explainAccount(err)
	}
	return pickerRows(apps), nil
}

func (e *connectorEnv) linkRow(conn connectors.AccountConnection, r pickerRow, label string) (connectors.LinkedKeyset, error) {
	if r.keyset.LinkedKeysetID != nil {
		return e.alreadyLinked(*r.keyset.LinkedKeysetID)
	}
	if label == "" {
		label = connectors.DefaultLinkLabel(r.app.Name, r.keyset.Name, r.keyset.ID)
	}
	k, err := e.api.LinkKeyset(e.ctx, connectors.LinkKeysetRequest{
		Source:         connectors.LinkSourceAdminAPI,
		Label:          label,
		ConnectionID:   conn.ID,
		PubnubKeysetID: r.keyset.ID,
	})
	if connectors.IsAlreadyLinked(err) {
		return e.linkedBySubscribeKey(r.keyset.SubscribeKey)
	}
	if err != nil {
		return connectors.LinkedKeyset{}, e.explain(err, "")
	}
	e.printf("Linked %q: %s.\n", termsafe.Text(k.Label), featureSummary(k.Features))
	return k, nil
}

func (e *connectorEnv) alreadyLinked(linkedID string) (connectors.LinkedKeyset, error) {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return connectors.LinkedKeyset{}, e.explain(err, "")
	}
	k, err := connectors.ResolveKeyset(keysets, linkedID)
	if err != nil {
		return k, fmt.Errorf("the keyset is linked but cannot be used (%s). See `blocks connector keyset list --all`", termsafe.Text(err.Error()))
	}
	e.reportAlreadyLinked(k)
	return k, nil
}

func (e *connectorEnv) linkedBySubscribeKey(subscribeKey string) (connectors.LinkedKeyset, error) {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return connectors.LinkedKeyset{}, e.explain(err, "")
	}
	k, ok := connectors.FindLinkedBySubscribeKey(keysets, subscribeKey)
	if !ok {
		return k, errors.New("the keyset is already linked but is not active. See `blocks connector keyset list --all`")
	}
	e.reportAlreadyLinked(k)
	return k, nil
}

func featureSummary(f connectors.KeysetFeatures) string {
	return fmt.Sprintf("Access Manager %s, Message Persistence %s, Presence %s",
		onOff(f.AccessManager), onOff(f.Persistence), onOff(f.Presence))
}

func (e *connectorEnv) reportAlreadyLinked(k connectors.LinkedKeyset) {
	e.printf("Keyset %q is already linked.\n", termsafe.Text(k.Label))
}
