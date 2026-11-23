package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

var (
	connectorAccountListAll  bool
	connectorAccountListJSON bool
	connectorAccountYes      bool
)

func init() {
	connectorCmd.AddCommand(connectorAccountCmd)
	connectorAccountCmd.AddCommand(connectorAccountConnectCmd, connectorAccountListCmd, connectorAccountDisconnectCmd)
	connectorAccountListCmd.Flags().BoolVar(&connectorAccountListAll, "all", false, "Include dead connections of other users")
	connectorAccountListCmd.Flags().BoolVar(&connectorAccountListJSON, "json", false, "Print JSON")
	connectorAccountDisconnectCmd.Flags().BoolVar(&connectorAccountYes, "yes", false, "Skip the confirmation prompt")
}

var connectorAccountCmd = &cobra.Command{
	Use:   "account",
	Short: "Manage the PubNub accounts connected to your org",
}

var connectorAccountConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect a PubNub account in the browser",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{banner: true}, func(e *connectorEnv) error {
			if !e.interactive {
				return errors.New("connecting a PubNub account needs a browser: run `blocks connector account connect` in a terminal")
			}
			_, err := e.connectInBrowser()
			return err
		})
	},
}

var connectorAccountListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the PubNub accounts connected to your org",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{quiet: connectorAccountListJSON}, func(e *connectorEnv) error {
			return e.listAccounts(connectorAccountListAll, connectorAccountListJSON)
		})
	},
}

var connectorAccountDisconnectCmd = &cobra.Command{
	Use:   "disconnect [id|name]",
	Short: "Disconnect a PubNub account you connected",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{banner: true, yes: connectorAccountYes}, func(e *connectorEnv) error {
			return e.disconnectAccount(args, connectorAccountYes)
		})
	},
}

func (e *connectorEnv) listAccounts(all, asJSON bool) error {
	conns, err := e.api.ListAccountConnections(e.ctx)
	if err != nil {
		return e.explainAccount(err)
	}
	conns = connectors.VisibleConnections(conns, all)
	if asJSON {
		return printConnectorJSON(conns)
	}
	if len(conns) == 0 {
		e.println("No PubNub accounts are connected. Run `blocks connector account connect`.")
		return nil
	}
	tw := newConnectorTable(e.out)
	tableRow(tw, "ID", "NAME", "STATUS", "CONNECTED BY", "MINE")
	for _, c := range conns {
		tableRow(tw, c.ID, c.Name, c.Status, c.ConnectedBy.Name, yesNo(c.IsMine))
	}
	return tw.Flush()
}

func (e *connectorEnv) disconnectAccount(args []string, yes bool) error {
	conns, err := e.api.ListAccountConnections(e.ctx)
	if err != nil {
		return e.explainAccount(err)
	}
	conn, err := e.connectionToDisconnect(conns, args)
	if err != nil {
		return err
	}
	if err := connectors.CheckDisconnectable(conn); err != nil {
		return fmt.Errorf("only the person who connected %q can disconnect it", termsafe.Text(conn.Name))
	}
	name := termsafe.Text(conn.Name)
	ok, err := e.requireConfirmation(yes, fmt.Sprintf("Disconnect PubNub account %q?", name),
		fmt.Sprintf("refusing to disconnect %q without confirmation: pass --yes", name))
	if err != nil {
		return err
	}
	if !ok {
		e.println("Cancelled: nothing was disconnected.")
		return nil
	}
	if err := e.api.DisconnectAccount(e.ctx, conn.ID); err != nil {
		return e.explainAccount(err)
	}
	e.printf("Disconnected %q\n", termsafe.Text(conn.Name))
	return nil
}

func (e *connectorEnv) connectionToDisconnect(conns []connectors.AccountConnection, args []string) (connectors.AccountConnection, error) {
	if len(args) == 1 {
		conn, err := connectors.ResolveConnection(conns, args[0])
		if err != nil {
			return conn, fmt.Errorf("%s. See `blocks connector account list`", termsafe.Text(err.Error()))
		}
		return conn, nil
	}
	mine := connectors.VisibleConnections(conns, false)
	var own []connectors.AccountConnection
	for _, c := range mine {
		if c.IsMine {
			own = append(own, c)
		}
	}
	switch {
	case len(own) == 0:
		return connectors.AccountConnection{}, errors.New("you have no PubNub account connection to disconnect. See `blocks connector account list`")
	case len(own) == 1:
		return own[0], nil
	case !e.interactive:
		return connectors.AccountConnection{}, errors.New("you have several PubNub account connections: pass the id or name of the one to disconnect")
	}
	i, err := e.selectOne("PubNub account", connectionOptions(own))
	if err != nil {
		return connectors.AccountConnection{}, err
	}
	return own[i], nil
}

func connectionOptions(conns []connectors.AccountConnection) []string {
	opts := make([]string, len(conns))
	for i, c := range conns {
		opts[i] = fmt.Sprintf("%s (connected by %s)", termsafe.Text(c.Name), termsafe.Text(c.ConnectedBy.Name))
	}
	return opts
}

func (e *connectorEnv) chooseConnection(ref string) (connectors.ConnectionChoice, error) {
	conns, err := e.api.ListAccountConnections(e.ctx)
	if err != nil {
		return connectors.ConnectionChoice{}, e.explainAccount(err)
	}
	choice, err := connectors.ChooseConnection(conns, ref)
	switch {
	case errors.Is(err, connectors.ErrConnectionDead):
		return choice, errors.New("that PubNub account connection no longer works. Run `blocks connector account connect`")
	case err != nil:
		return choice, fmt.Errorf("%s. See `blocks connector account list`", termsafe.Text(err.Error()))
	case len(choice.Candidates) > 0:
		return e.pickAmongConnections(choice)
	}
	return choice, nil
}

func (e *connectorEnv) pickAmongConnections(choice connectors.ConnectionChoice) (connectors.ConnectionChoice, error) {
	if !e.interactive {
		return choice, fmt.Errorf("several PubNub accounts are connected (%s): pass --account with an id or name",
			termsafe.Text(connectionNames(choice.Candidates)))
	}
	i, err := e.selectOne("PubNub account", connectionOptions(choice.Candidates))
	if err != nil {
		return choice, err
	}
	return connectors.ConnectionChoice{Use: &choice.Candidates[i]}, nil
}

func connectionNames(conns []connectors.AccountConnection) string {
	names := ""
	for i, c := range conns {
		if i > 0 {
			names += ", "
		}
		names += fmt.Sprintf("%s (%s)", c.Name, c.ID)
	}
	return names
}

func (e *connectorEnv) announceConnection(conn connectors.AccountConnection) {
	e.printf("Using PubNub account %q, connected by %s.\n", termsafe.Text(conn.Name), termsafe.Text(conn.ConnectedBy.Name))
}

func (e *connectorEnv) needBrowserError(choice connectors.ConnectionChoice, keysetHint bool) error {
	state := "no PubNub account is connected in " + orgLabel()
	if choice.OwnDead != nil {
		state = "your PubNub account connection in " + orgLabel() + " no longer works"
	}
	msg := state + ". Run `blocks connector account connect` once in a terminal with a browser"
	if keysetHint {
		msg += ", or use --keyset with a keyset already linked"
	}
	return errors.New(msg)
}

func (e *connectorEnv) connectInBrowser() (connectors.AccountConnection, error) {
	orgID := clictx.OrgID()
	if orgID == "" {
		return connectors.AccountConnection{}, errNoOrgID
	}
	connectURL, err := connectPageURL(orgID)
	if err != nil {
		return connectors.AccountConnection{}, err
	}
	before, err := e.api.ListAccountConnections(e.ctx)
	if err != nil {
		return connectors.AccountConnection{}, e.explainAccount(err)
	}
	waitCtx, stop := signal.NotifyContext(e.ctx, os.Interrupt)
	defer stop()
	e.printf("Opening the browser to connect a PubNub account in %s.\n  %s\n", orgLabel(), termsafe.Text(connectURL))
	e.println("On that page open the PubNub accounts tab and choose Connect PubNub account.")
	if err := openBrowser(connectURL); err != nil {
		e.printf("Could not open the browser. Visit:\n  %s\n", termsafe.Text(connectURL))
	}
	e.println("Waiting for the connection (Ctrl-C to stop)...")
	waiter := connectors.Waiter{Snapshot: connectors.NewSnapshot(before), Poll: e.api.ListAccountConnections}
	conn, err := waiter.Wait(waitCtx)
	var timeout *connectors.WaitTimeoutError
	switch {
	case errors.Is(err, context.Canceled):
		return conn, errors.New("stopped waiting; nothing was connected")
	case errors.As(err, &timeout):
		return conn, fmt.Errorf("%s in %s: %s", termsafe.Text(timeout.Error()), orgLabel(), connectors.TimeoutHint)
	case err != nil:
		return conn, e.explainAccount(err)
	}
	e.printf("connected: %q\n", termsafe.Text(conn.Name))
	return conn, nil
}

var errNoOrgID = errors.New("cannot tell which org this login belongs to: run `blocks login`")

func connectPageURL(orgID string) (string, error) {
	base, err := resolveDashboardURL()
	if err != nil {
		return "", err
	}
	origin, err := dashboardURL(base, "")
	if err != nil {
		return "", err
	}
	q := url.Values{"org": {orgID}, "connect": {"cli"}}
	return origin + "/manage/pubnub-connectors?" + q.Encode(), nil
}

func (e *connectorEnv) confirmReconnect(dead connectors.AccountConnection) bool {
	return e.confirm(fmt.Sprintf("Your PubNub account connection %q no longer works. Reconnect it in the browser?", termsafe.Text(dead.Name)))
}
