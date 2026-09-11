package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

// Every value these commands print comes from outside the CLI — an address or slug
// the caller typed, or a name, email, slug, link or error message the deployment
// returned — and one of them shares a screen with the removal these commands
// perform. termsafe.Text is applied at each print site so no such value can emit a
// control sequence that rewrites the surrounding output; the request payloads and
// URLs keep using the raw values. The one exception is the context banner's subject,
// which is handed over raw because clictx escapes every half of that line itself.
//
// None of these commands is scoped by the organization the credential belongs to:
// the deployment authorizes a send or a revoke by ownership of the named agent, and
// an accept by being the invitation's recipient. So the banner names the subject
// authorization turns on — this agent, this grantee — rather than an organization
// that decides nothing about which agent can be shared or unshared. See
// clictx.Banner.

var inviteSendEmail string
var inviteSendOrg string
var inviteRevokeEmail string
var inviteRevokeOrg string

func init() {
	rootCmd.AddCommand(inviteCmd)

	inviteCmd.AddCommand(inviteSendCmd)
	inviteSendCmd.Flags().StringVar(&inviteSendEmail, "email", "", "Email address of the invitee")
	inviteSendCmd.Flags().StringVar(&inviteSendOrg, "org", "", "Organization slug to invite")

	inviteCmd.AddCommand(inviteListCmd)
	inviteCmd.AddCommand(inviteAcceptCmd)

	inviteCmd.AddCommand(inviteRevokeCmd)
	inviteRevokeCmd.Flags().StringVar(&inviteRevokeEmail, "email", "", "Email of the user to revoke")
	inviteRevokeCmd.Flags().StringVar(&inviteRevokeOrg, "org", "", "Organization ID to revoke")

	inviteCmd.AddCommand(inviteGrantsCmd)
}

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Manage private agent invitations and grants",
	Long:  "Send, list, accept, and revoke access invitations for private agents.",
}

// agentNameArg accepts exactly one argument and requires it to be a bare registry
// agent name. Every command that takes one uses it, because each of them puts that
// argument straight into a request path — and a name carrying '/', '?' or '#'
// silently changes which endpoint is called. For `send` and `revoke` that is worse
// than a bad request: the safety banner describes the name as supplied, so the
// operator would read a confirmation for one agent while the request went somewhere
// else. It is an Args validator rather than a check inside RunE so it runs before
// the banner is printed, and so the two can never disagree.
//
// The pattern is wizard.ValidateAgentName's — the registry's own — so there is one
// spelling of what an agent name is rather than a second one here that could drift
// from it. Validation is the defence; agentPathSegment is the belt.
func agentNameArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return err
	}
	return wizard.ValidateAgentName(args[0])
}

// agentPathSegment renders a value for use as a single path segment. It backs up
// agentNameArg for the agent name, and is the only defence for a grant id, which
// comes from the deployment's own response rather than from a validated argument.
func agentPathSegment(v string) string { return url.PathEscape(v) }

var inviteSendCmd = &cobra.Command{
	Use:   "send <agentName>",
	Short: "Send an invitation to access a private agent",
	Args:  agentNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		if inviteSendEmail == "" && inviteSendOrg == "" {
			return fmt.Errorf("either --email or --org is required")
		}
		if inviteSendEmail != "" && inviteSendOrg != "" {
			return fmt.Errorf("--email and --org are mutually exclusive")
		}
		clictx.PrintBanner(clictx.ActsOn(inviteSubject(args[0], inviteSendEmail, inviteSendOrg)))
		return runInviteSend(args[0])
	},
}

var inviteListCmd = &cobra.Command{
	Use:   "list <agentName>",
	Short: "List unaccepted invitations for a private agent",
	Args:  agentNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInviteList(args[0])
	},
}

var inviteAcceptCmd = &cobra.Command{
	Use:   "accept <token>",
	Short: "Accept an agent invitation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Accepting is authorized by being the invitation's recipient, and the only
		// subject available before the request is the token the caller just typed —
		// printing it back adds nothing and it is a secret. So the banner states the
		// deployment alone, and the agent the invitation was for is named in the
		// success line, which is the first point at which the CLI knows it.
		clictx.PrintBanner(clictx.OrgIsNotTheScope())
		return runInviteAccept(args[0])
	},
}

var inviteRevokeCmd = &cobra.Command{
	Use:   "revoke <agentName>",
	Short: "Revoke access to a private agent",
	Args:  agentNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		if inviteRevokeEmail == "" && inviteRevokeOrg == "" {
			return fmt.Errorf("either --email or --org is required")
		}
		if inviteRevokeEmail != "" && inviteRevokeOrg != "" {
			return fmt.Errorf("--email and --org are mutually exclusive")
		}
		clictx.PrintBanner(clictx.ActsOn(inviteSubject(args[0], inviteRevokeEmail, inviteRevokeOrg)))
		return runInviteRevoke(args[0])
	},
}

// inviteSubject names what a send or a revoke acts on: the agent whose ownership
// authorizes the change, and the grantee whose access it changes. Both commands
// share one shape because both turn on exactly that pair — the verb is already on
// the command line the operator typed. Values are returned raw for the banner to
// escape.
func inviteSubject(agentName, email, org string) string {
	switch {
	case email != "":
		return "agent " + agentName + " → " + email
	case org != "":
		return "agent " + agentName + " → org " + org
	}
	return "agent " + agentName
}

var inviteGrantsCmd = &cobra.Command{
	Use:   "grants <agentName>",
	Short: "List active grants for a private agent",
	Args:  agentNameArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInviteGrants(args[0])
	},
}

func runInviteSend(agentName string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return backendNotConfigured("BLOCKS_BACKEND_URL must be set")
	}

	payload := map[string]interface{}{}
	if inviteSendEmail != "" {
		payload["email"] = inviteSendEmail
	}
	if inviteSendOrg != "" {
		payload["targetOrgSlug"] = inviteSendOrg
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	sendURL := fmt.Sprintf("%s/api/v1/agents/%s/invitations", backendURL, agentPathSegment(agentName))
	req, err := http.NewRequest("POST", sendURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return handleErrorResponse(resp)
	}

	target := termsafe.Text(inviteSendEmail)
	if inviteSendOrg != "" {
		target = "org " + termsafe.Text(inviteSendOrg)
	}

	// The invitation is created either way; `notified` says how many addresses were
	// actually emailed. Absent means the server cannot report it, which is not the
	// same as zero — an older server did email the invitation and had no field to
	// say so — so only an explicit zero is reported as undelivered. The link is
	// then the only way in, so it is printed rather than discarded.
	var result struct {
		Notified  *int   `json:"notified"`
		InviteURL string `json:"inviteUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("Invitation sent to %s\n", target)
		return nil
	}

	if result.Notified != nil && *result.Notified == 0 {
		fmt.Printf("Invitation created for %s, but it could not be emailed.\n", target)
		if result.InviteURL != "" {
			fmt.Printf("Share this link instead: %s\n", termsafe.Text(result.InviteURL))
		}
		return nil
	}

	fmt.Printf("Invitation sent to %s\n", target)
	return nil
}

func runInviteList(agentName string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return backendNotConfigured("BLOCKS_BACKEND_URL must be set")
	}

	listURL := fmt.Sprintf("%s/api/v1/agents/%s/invitations", backendURL, agentPathSegment(agentName))
	req, err := http.NewRequest("GET", listURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return handleErrorResponse(resp)
	}

	var result struct {
		Invitations []struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			Scope     string `json:"scope"`
			ExpiresAt string `json:"expiresAt"`
			CreatedAt string `json:"createdAt"`
		} `json:"invitations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Invitations) == 0 {
		fmt.Println("No unaccepted invitations.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tEMAIL\tSCOPE\tCREATED\tEXPIRES")
	for _, inv := range result.Invitations {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			termsafe.Text(inv.ID), termsafe.Text(inv.Email), termsafe.Text(inv.Scope),
			termsafe.Text(inv.CreatedAt), termsafe.Text(inv.ExpiresAt))
	}
	w.Flush()
	return nil
}

func runInviteAccept(token string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return backendNotConfigured("BLOCKS_BACKEND_URL must be set")
	}

	payload := map[string]string{"token": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	acceptURL := fmt.Sprintf("%s/api/v1/agent-invitations/accept", backendURL)
	req, err := http.NewRequest("POST", acceptURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return handleErrorResponse(resp)
	}

	var result struct {
		AgentName string `json:"agentName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	fmt.Printf("Access granted to %s\n", termsafe.Text(result.AgentName))
	return nil
}

func runInviteRevoke(agentName string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return backendNotConfigured("BLOCKS_BACKEND_URL must be set")
	}

	// First list grants to find the one matching the email/org
	grantsURL := fmt.Sprintf("%s/api/v1/agents/%s/grants", backendURL, agentPathSegment(agentName))
	req, err := http.NewRequest("GET", grantsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return handleErrorResponse(resp)
	}

	var grantsResult struct {
		Grants []struct {
			ID          string `json:"id"`
			Scope       string `json:"scope"`
			GranteeUser *struct {
				Email string `json:"email"`
			} `json:"granteeUser"`
			GranteeOrg *struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			} `json:"granteeOrg"`
		} `json:"grants"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&grantsResult); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	var grantID string
	for _, g := range grantsResult.Grants {
		if inviteRevokeEmail != "" && g.GranteeUser != nil && g.GranteeUser.Email == inviteRevokeEmail {
			grantID = g.ID
			break
		}
		if inviteRevokeOrg != "" && g.GranteeOrg != nil &&
			(g.GranteeOrg.ID == inviteRevokeOrg || g.GranteeOrg.Slug == inviteRevokeOrg) {
			grantID = g.ID
			break
		}
	}

	if grantID == "" {
		if inviteRevokeEmail != "" {
			return fmt.Errorf("no active grant found for email %s", termsafe.Text(inviteRevokeEmail))
		}
		return fmt.Errorf("no active grant found for org %s", termsafe.Text(inviteRevokeOrg))
	}

	// Delete the grant
	deleteURL := fmt.Sprintf("%s/api/v1/agents/%s/grants/%s", backendURL, agentPathSegment(agentName), agentPathSegment(grantID))
	delReq, err := http.NewRequest("DELETE", deleteURL, nil)
	if err != nil {
		return err
	}
	delReq.Header.Set("Authorization", "Bearer "+apiKey)

	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer delResp.Body.Close()

	if delResp.StatusCode < 200 || delResp.StatusCode >= 300 {
		return handleErrorResponse(delResp)
	}

	if inviteRevokeEmail != "" {
		fmt.Printf("Access revoked for %s\n", termsafe.Text(inviteRevokeEmail))
	} else {
		fmt.Printf("Access revoked for org %s\n", termsafe.Text(inviteRevokeOrg))
	}
	return nil
}

func runInviteGrants(agentName string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return backendNotConfigured("BLOCKS_BACKEND_URL must be set")
	}

	grantsURL := fmt.Sprintf("%s/api/v1/agents/%s/grants", backendURL, agentPathSegment(agentName))
	req, err := http.NewRequest("GET", grantsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return handleErrorResponse(resp)
	}

	var result struct {
		Grants []struct {
			ID          string `json:"id"`
			Scope       string `json:"scope"`
			CreatedAt   string `json:"createdAt"`
			GranteeUser *struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"granteeUser"`
			GranteeOrg *struct {
				Name string `json:"name"`
				Slug string `json:"slug"`
			} `json:"granteeOrg"`
		} `json:"grants"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Grants) == 0 {
		fmt.Println("No active grants.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSCOPE\tGRANTEE\tCREATED")
	for _, g := range result.Grants {
		grantee := ""
		if g.GranteeUser != nil {
			grantee = g.GranteeUser.Email
		} else if g.GranteeOrg != nil {
			grantee = g.GranteeOrg.Slug
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			termsafe.Text(g.ID), termsafe.Text(g.Scope), termsafe.Text(grantee), termsafe.Text(g.CreatedAt))
	}
	w.Flush()
	return nil
}

func handleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil {
		msg := errResp.Error
		if msg == "" {
			msg = errResp.Message
		}
		if msg != "" {
			return inviteRequestFailedError(resp.StatusCode, termsafe.Text(msg))
		}
	}
	return inviteRequestFailedError(resp.StatusCode, "")
}
