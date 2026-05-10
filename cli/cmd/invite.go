package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

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

var inviteSendCmd = &cobra.Command{
	Use:   "send <agentName>",
	Short: "Send an invitation to access a private agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inviteSendEmail == "" && inviteSendOrg == "" {
			return fmt.Errorf("either --email or --org is required")
		}
		if inviteSendEmail != "" && inviteSendOrg != "" {
			return fmt.Errorf("--email and --org are mutually exclusive")
		}
		return runInviteSend(args[0])
	},
}

var inviteListCmd = &cobra.Command{
	Use:   "list <agentName>",
	Short: "List pending invitations for a private agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInviteList(args[0])
	},
}

var inviteAcceptCmd = &cobra.Command{
	Use:   "accept <token>",
	Short: "Accept an agent invitation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInviteAccept(args[0])
	},
}

var inviteRevokeCmd = &cobra.Command{
	Use:   "revoke <agentName>",
	Short: "Revoke access to a private agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inviteRevokeEmail == "" && inviteRevokeOrg == "" {
			return fmt.Errorf("either --email or --org is required")
		}
		if inviteRevokeEmail != "" && inviteRevokeOrg != "" {
			return fmt.Errorf("--email and --org are mutually exclusive")
		}
		return runInviteRevoke(args[0])
	},
}

var inviteGrantsCmd = &cobra.Command{
	Use:   "grants <agentName>",
	Short: "List active grants for a private agent",
	Args:  cobra.ExactArgs(1),
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
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
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

	url := fmt.Sprintf("%s/api/v1/agents/%s/invitations", backendURL, agentName)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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

	if inviteSendOrg != "" {
		fmt.Printf("Invitation sent to org %s\n", inviteSendOrg)
	} else {
		fmt.Printf("Invitation sent to %s\n", inviteSendEmail)
	}
	return nil
}

func runInviteList(agentName string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
	}

	url := fmt.Sprintf("%s/api/v1/agents/%s/invitations", backendURL, agentName)
	req, err := http.NewRequest("GET", url, nil)
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
		fmt.Println("No pending invitations.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tEMAIL\tSCOPE\tCREATED\tEXPIRES")
	for _, inv := range result.Invitations {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			inv.ID, inv.Email, inv.Scope, inv.CreatedAt, inv.ExpiresAt)
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
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
	}

	payload := map[string]string{"token": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/agent-invitations/accept", backendURL)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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

	fmt.Printf("Access granted to %s\n", result.AgentName)
	return nil
}

func runInviteRevoke(agentName string) error {
	apiKey, err := loadCredentials()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
	}

	// First list grants to find the one matching the email/org
	grantsURL := fmt.Sprintf("%s/api/v1/agents/%s/grants", backendURL, agentName)
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
			return fmt.Errorf("no active grant found for email %s", inviteRevokeEmail)
		}
		return fmt.Errorf("no active grant found for org %s", inviteRevokeOrg)
	}

	// Delete the grant
	deleteURL := fmt.Sprintf("%s/api/v1/agents/%s/grants/%s", backendURL, agentName, grantID)
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
		fmt.Printf("Access revoked for %s\n", inviteRevokeEmail)
	} else {
		fmt.Printf("Access revoked for org %s\n", inviteRevokeOrg)
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
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
	}

	url := fmt.Sprintf("%s/api/v1/agents/%s/grants", backendURL, agentName)
	req, err := http.NewRequest("GET", url, nil)
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", g.ID, g.Scope, grantee, g.CreatedAt)
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
			return fmt.Errorf("request failed (HTTP %d): %s", resp.StatusCode, msg)
		}
	}
	return fmt.Errorf("request failed: HTTP %d", resp.StatusCode)
}
