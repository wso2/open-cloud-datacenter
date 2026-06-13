package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newAdminAgentTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-token",
		Short: "Manage dc-agent bootstrap tokens",
	}
	cmd.AddCommand(newAdminAgentTokenCreateCmd())
	return cmd
}

func newAdminAgentTokenCreateCmd() *cobra.Command {
	var (
		region     string
		zone       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a dc-agent token for a region/zone (platform-admin only)",
		Long: `Mint a bearer token that a dc-agent uses to connect to the control plane.

The token is for the agent that runs inside a Harvester cluster identified by
--region / --zone. If that region/zone is not yet known to dc-api it is
registered as a side effect (metadata only — the Harvester/Rancher bootstrap
itself stays with Terraform). The raw token is printed ONCE and is never
recoverable afterwards.

Names must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$ (lowercase, starts with a
letter, 2-32 chars).

Examples:
  dcctl admin agent-token create --region lk --zone zone-1
  dcctl admin agent-token create --region lk --zone zone-2 --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if region == "" || zone == "" {
				return fmt.Errorf("--region and --zone are required")
			}
			return runAdminAgentTokenCreate(cmd.Context(), region, zone, outputJSON)
		},
	}

	cmd.Flags().StringVar(&region, "region", "", "Region slug (e.g. lk) — required")
	cmd.Flags().StringVar(&zone, "zone", "", "Zone slug within the region (e.g. zone-1) — required")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runAdminAgentTokenCreate(ctx context.Context, region, zone string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	resp, err := apiClient.Typed.MintAgentTokenWithResponse(ctx, region, zone)
	if err != nil {
		return fmt.Errorf("POST /v1/admin/regions/%s/zones/%s/agent-token: %w", region, zone, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return apiErrorf(resp.StatusCode(), resp.Body)
	}
	tok := resp.JSON201

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tok)
	}

	fmt.Printf("Agent token created for region=%s zone=%s\n", tok.Region, tok.Zone)
	fmt.Println("Store it now — it is shown only once and cannot be recovered.")
	fmt.Println()
	fmt.Println("Set these on the dc-agent in that cluster:")
	fmt.Println("  DCAGENT_ENDPOINT=wss://<connect-endpoint>/v1/agent/ws")
	fmt.Printf("  DCAGENT_TOKEN=%s\n", tok.Token)
	fmt.Printf("  DCAGENT_REGION=%s\n", tok.Region)
	fmt.Printf("  DCAGENT_ZONE=%s\n", tok.Zone)
	return nil
}
