// Package peering — VNet peering creation command.
//
// `dcctl peering create --vnet <vnet> --peer-vnet <peer> --name <name>`
//
// Both VNets must be ACTIVE and in the same region. Their address spaces must
// not overlap. The server enforces all constraints; the CLI passes them through.
package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

type peeringFlags struct {
	vnet                  string
	peerVNet              string
	name                  string
	allowForwardedTraffic bool
	outputJSON            bool
	noWait                bool
}

func newCreateCmd() *cobra.Command {
	flags := &peeringFlags{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a VNet peering",
		Long: `Create a peering between two VNets to allow traffic to flow between them.

Both VNets must be ACTIVE and must belong to the same tenant and the same region.
Their address spaces must not overlap. Only one peering may exist per VNet pair.

Note: --allow-forwarded-traffic is accepted but is a no-op in M2.
The server will include a warning field in the response if this flag is set.

Example:
  dcctl peering create --vnet vnet-a --peer-vnet vnet-b --name peer-a-b
  dcctl peering create --vnet vnet-a --peer-vnet vnet-b --name peer-a-b --no-wait`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			projectFlag, _ := cmd.Root().PersistentFlags().GetString("project")
			projectID, err := dcconfig.GetProjectID(projectFlag, tenantID)
			if err != nil {
				return err
			}
			return runCreatePeering(cmd.Context(), tenantID, projectID, flags)
		},
	}

	cmd.Flags().StringVar(&flags.vnet, "vnet", "", "Initiating VNet name or ID (required)")
	cmd.Flags().StringVar(&flags.peerVNet, "peer-vnet", "", "Peer VNet name or ID (required)")
	cmd.Flags().StringVar(&flags.name, "name", "", "Peering name (required)")
	cmd.Flags().BoolVar(&flags.allowForwardedTraffic, "allow-forwarded-traffic", false, "Allow forwarded traffic (no-op in M2)")
	cmd.Flags().BoolVar(&flags.outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "Return immediately without waiting for the peering to become active")

	cmd.MarkFlagRequired("vnet")      //nolint:errcheck
	cmd.MarkFlagRequired("peer-vnet") //nolint:errcheck
	cmd.MarkFlagRequired("name")      //nolint:errcheck

	return cmd
}

func runCreatePeering(ctx context.Context, tenantID, projectID string, flags *peeringFlags) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, flags.vnet)
	if err != nil {
		return fmt.Errorf("resolve --vnet: %w", err)
	}
	peerIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, flags.peerVNet)
	if err != nil {
		return fmt.Errorf("resolve --peer-vnet: %w", err)
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	peerVnetID, err := uuid.Parse(peerIDStr)
	if err != nil {
		return fmt.Errorf("invalid peer VNet id %q: %w", peerIDStr, err)
	}

	body := dcapi.CreatePeeringRequest{
		Name:       flags.name,
		PeerVnetId: peerVnetID,
	}
	if flags.allowForwardedTraffic {
		t := true
		body.AllowForwardedTraffic = &t
	}

	fmt.Printf("Creating peering %q between %q and %q...\n", flags.name, flags.vnet, flags.peerVNet)
	resp, err := apiClient.Typed.CreatePeeringWithResponse(ctx, tenantID, projectID, vnetID, body)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/vnets/%s/peerings: %w", tenantID, vnetIDStr, err)
	}
	if resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if flags.outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON202)
	}

	if warn, _ := resp.JSON202.Resource["warning"].(string); warn != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warn)
	}

	peeringIDStr, _ := resp.JSON202.Resource["id"].(string)
	if peeringIDStr == "" {
		fmt.Println("Peering accepted but no id in response — check 'dcctl peering list --vnet …'.")
		return nil
	}
	peeringID, err := uuid.Parse(peeringIDStr)
	if err != nil {
		return fmt.Errorf("server returned non-UUID id %q: %w", peeringIDStr, err)
	}

	if flags.noWait {
		fmt.Printf("Peering accepted (PENDING). Check status with:\n  dcctl peering get --vnet %s %s\n", vnetIDStr, peeringID)
		return nil
	}

	final, err := pollPeeringUntilDone(ctx, tenantID, projectID, apiClient, vnetID, peeringID)
	if err != nil {
		return err
	}
	fmt.Println()
	printPeeringCompact(final)
	return nil
}

func printPeeringCompact(p *dcapi.Peering) {
	fmt.Printf("  ID:          %s\n", p.Id)
	fmt.Printf("  VNet:        %s\n", p.VnetId)
	fmt.Printf("  Peer VNet:   %s\n", p.PeerVnetId)
	fmt.Printf("  Name:        %s\n", p.Name)
	fmt.Printf("  Status:      %s\n", p.Status)
}

func pollPeeringUntilDone(ctx context.Context, tenantID, projectID string, apiClient *client.Client, vnetID, peeringID uuid.UUID) (*dcapi.Peering, error) {
	const (
		pollInterval = 2 * time.Second
		timeout      = 60 * time.Second
	)
	deadline := time.Now().Add(timeout)
	fmt.Print("Waiting for Peering to become active")

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		fmt.Print(".")

		resp, err := apiClient.Typed.GetPeeringWithResponse(ctx, tenantID, projectID, vnetID, peeringID)
		if err != nil || resp.JSON200 == nil {
			continue
		}
		p := resp.JSON200
		switch p.Status {
		case dcapi.ResourceStatusACTIVE:
			fmt.Println(" done!")
			return p, nil
		case dcapi.ResourceStatusFAILED:
			fmt.Println()
			return nil, fmt.Errorf("Peering provisioning failed: %s", cliutil.DerefOrDash(p.Message))
		}
	}

	fmt.Println()
	return nil, fmt.Errorf("timed out after %s — check status with: dcctl peering get --vnet %s %s", timeout, vnetID, peeringID)
}
