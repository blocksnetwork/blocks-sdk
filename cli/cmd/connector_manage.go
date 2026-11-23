package cmd

import (
	"errors"
	"fmt"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

var (
	connectorListAll   bool
	connectorListJSON  bool
	connectorEditOpts  connectorShape
	connectorRevokeYes bool
)

func init() {
	connectorCmd.AddCommand(connectorListCmd, connectorEditCmd, connectorRevokeCmd)
	connectorListCmd.Flags().BoolVar(&connectorListAll, "all", false, "Include revoked Connectors")
	connectorListCmd.Flags().BoolVar(&connectorListJSON, "json", false, "Print JSON")
	addShapeFlags(connectorEditCmd, &connectorEditOpts)
	connectorRevokeCmd.Flags().BoolVar(&connectorRevokeYes, "yes", false, "Skip the confirmation prompt")
}

var connectorListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the Connectors in your org",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{quiet: connectorListJSON}, func(e *connectorEnv) error {
			return e.listConnectors(connectorListAll, connectorListJSON)
		})
	},
}

var connectorEditCmd = &cobra.Command{
	Use:   "edit <agent-name>",
	Short: "Change a Connector's name, description, channels or config",
	Long: "Change a Connector. Only the flags you pass are changed. Its keyset, kind and agent name cannot be changed: revoke it and create a new one.\n\n" +
		"--channel-template and --raw-channel/--raw-pattern switch how the Connector is addressed. The raw flags replace the whole raw selector: pass every channel and pattern the Connector should keep.",
	Args: agentNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		o := connectorEditOpts
		o.captureChanged(cmd)
		return withConnectorEnv(cmd, sessionOpts{banner: true}, func(e *connectorEnv) error {
			return e.editConnector(args[0], o)
		})
	},
}

var connectorRevokeCmd = &cobra.Command{
	Use:   "revoke <agent-name>",
	Short: "Revoke a Connector",
	Args:  agentNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withConnectorEnv(cmd, sessionOpts{banner: true}, func(e *connectorEnv) error {
			return e.revokeConnector(args[0], connectorRevokeYes)
		})
	},
}

func (e *connectorEnv) listConnectors(all, asJSON bool) error {
	exposures, err := e.api.ListExposures(e.ctx)
	if err != nil {
		return e.explain(err, "")
	}
	exposures = connectors.VisibleExposures(exposures, all)
	if exposures == nil {
		exposures = []connectors.Exposure{}
	}
	if asJSON {
		return printConnectorJSON(exposures)
	}
	if len(exposures) == 0 {
		e.println("No Connectors. Create one with `blocks connector create`.")
		return nil
	}
	tw := newConnectorTable(e.out)
	tableRow(tw, "AGENT", "KIND", "ADDRESSING", "DISPLAY NAME", "STATUS")
	for _, x := range exposures {
		tableRow(tw, x.AgentName, kindLabel(x.Kind), addressing(x), x.DisplayName, exposureStatus(x))
	}
	return tw.Flush()
}

func kindLabel(kind string) string {
	if name, ok := kindNames[kind]; ok {
		return name
	}
	return kind
}

func exposureStatus(x connectors.Exposure) string {
	if x.Revoked() {
		return "revoked"
	}
	return "active"
}

func (e *connectorEnv) activeConnector(name string) (connectors.Exposure, error) {
	exposures, err := e.api.ListExposures(e.ctx)
	if err != nil {
		return connectors.Exposure{}, e.explain(err, name)
	}
	x, err := connectors.ResolveExposureByAgentName(exposures, name)
	return x, explainConnectorLookup(name, err)
}

func explainConnectorLookup(name string, err error) error {
	switch {
	case errors.Is(err, connectors.ErrExposureRevoked):
		return fmt.Errorf("the Connector %s is revoked and cannot be changed. See `blocks connector list --all`", shownName(name))
	case errors.Is(err, connectors.ErrNotFound):
		return fmt.Errorf("no active Connector named %s. See `blocks connector list`", shownName(name))
	case err != nil:
		return fmt.Errorf("%s", termsafe.Text(err.Error()))
	}
	return nil
}

func (e *connectorEnv) editConnector(name string, s connectorShape) error {
	x, err := e.activeConnector(name)
	if err != nil {
		return err
	}
	req, err := editRequest(x, s)
	if err != nil {
		return err
	}
	if _, err := e.api.UpdateExposure(e.ctx, x.ID, req); err != nil {
		return e.explain(err, name)
	}
	e.printf("Updated Connector %s\n", shownName(name))
	return nil
}

func editRequest(x connectors.Exposure, s connectorShape) (connectors.UpdateExposureRequest, error) {
	var req connectors.UpdateExposureRequest
	if err := s.checkKind(x.Kind); err != nil {
		return req, err
	}
	if s.displayName != "" {
		req.DisplayName = &s.displayName
	}
	if s.description != "" {
		req.Description = &s.description
	}
	if s.channelTemplate != "" {
		req.ChannelTemplate = &s.channelTemplate
	}
	if s.raw() {
		req.RawSelector = s.rawSelector()
	}
	if s.configChanged() {
		cfg, err := s.config(x.Config)
		if err != nil {
			return req, err
		}
		req.Config = &cfg
	}
	if req == (connectors.UpdateExposureRequest{}) {
		return req, errors.New("nothing to change: pass at least one flag, see `blocks connector edit --help`")
	}
	return req, nil
}

func (e *connectorEnv) revokeConnector(name string, yes bool) error {
	exposures, err := e.api.ListExposures(e.ctx)
	if err != nil {
		return e.explain(err, name)
	}
	x, err := connectors.ResolveExposureByAgentName(exposures, name)
	if errors.Is(err, connectors.ErrExposureRevoked) {
		return e.retryRevoke(name, revokedNamed(exposures, name))
	}
	if err := explainConnectorLookup(name, err); err != nil {
		return err
	}
	if err := e.printRevokeNotice(x.LinkedKeysetID); err != nil {
		return e.explain(err, name)
	}
	prompt := fmt.Sprintf("Revoke Connector %s? Callers lose the access it gives.", shownName(name))
	ok, err := e.requireConfirmation(yes, prompt, fmt.Sprintf("refusing to revoke %s without confirmation: pass --yes", shownName(name)))
	if err != nil {
		return err
	}
	if !ok {
		e.println("Cancelled: nothing was revoked.")
		return nil
	}
	if _, err := e.api.RevokeExposure(e.ctx, x.ID); err != nil {
		return e.explain(err, name)
	}
	e.printf("Revoked Connector %s\n", shownName(name))
	return nil
}

func revokedNamed(exposures []connectors.Exposure, name string) []connectors.Exposure {
	var out []connectors.Exposure
	for _, x := range exposures {
		if x.AgentName == name && x.Revoked() {
			out = append(out, x)
		}
	}
	return out
}

func (e *connectorEnv) retryRevoke(name string, revoked []connectors.Exposure) error {
	for _, x := range revoked {
		if _, err := e.api.RevokeExposure(e.ctx, x.ID); err != nil {
			return e.explain(err, name)
		}
	}
	e.printf("Connector %s was already revoked. Retried revoking its PubNub tokens.\n", shownName(name))
	return nil
}

func (e *connectorEnv) printRevokeNotice(keysetID string) error {
	keysets, err := e.api.ListKeysets(e.ctx)
	if err != nil {
		return err
	}
	for _, ks := range keysets {
		if ks.ID == keysetID {
			if notice := connectors.RevokeNotice(ks); notice != "" {
				e.println(notice)
			}
		}
	}
	return nil
}
