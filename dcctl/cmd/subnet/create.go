// Package subnet — Subnet creation command.
package subnet

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

type subnetFlags struct {
	vnet       string
	name       string
	cidr       string
	outputJSON bool
	noWait     bool
}

func newCreateCmd() *cobra.Command {
	flags := &subnetFlags{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a subnet within a VNet",
		Long: `Create a subnet within an existing (ACTIVE) VNet.

The CIDR must be contained within one of the parent VNet's address space ranges
and must not overlap any existing sibling subnet in the same VNet.
The gateway defaults to the first usable host address (e.g. 10.10.1.1 for 10.10.1.0/24).

Example:
  dcctl subnet create --vnet vnet-a --name sub-a --cidr 10.10.1.0/24
  dcctl subnet create --vnet vnet-a --name sub-b --cidr 10.10.2.0/24 --no-wait`,
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
			return runCreateSubnet(cmd.Context(), tenantID, projectID, flags)
		},
	}

	cmd.Flags().StringVar(&flags.vnet, "vnet", "", "Parent VNet name or ID (required)")
	cmd.Flags().StringVar(&flags.name, "name", "", "Subnet name (required)")
	cmd.Flags().StringVar(&flags.cidr, "cidr", "", "Subnet CIDR (e.g. 10.10.1.0/24, required)")
	cmd.Flags().BoolVar(&flags.outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "Return immediately without waiting for the subnet to become active")

	cmd.MarkFlagRequired("vnet") //nolint:errcheck
	cmd.MarkFlagRequired("name") //nolint:errcheck
	cmd.MarkFlagRequired("cidr") //nolint:errcheck

	return cmd
}

func runCreateSubnet(ctx context.Context, tenantID, projectID string, flags *subnetFlags) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, flags.vnet)
	if err != nil {
		return fmt.Errorf("resolve --vnet: %w", err)
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}

	fmt.Printf("Creating subnet %q in VNet %q...\n", flags.name, flags.vnet)
	resp, err := apiClient.Typed.CreateSubnetWithResponse(ctx, tenantID, projectID, vnetID, dcapi.CreateSubnetRequest{
		Name: flags.name,
		Cidr: flags.cidr,
	})
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/vnets/%s/subnets: %w", tenantID, vnetIDStr, err)
	}
	if resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if flags.outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON202)
	}

	subnetIDStr, _ := resp.JSON202.Resource["id"].(string)
	if subnetIDStr == "" {
		fmt.Println("Subnet accepted but no id in response — check 'dcctl subnet list --vnet ...'.")
		return nil
	}
	subnetID, err := uuid.Parse(subnetIDStr)
	if err != nil {
		return fmt.Errorf("server returned non-UUID id %q: %w", subnetIDStr, err)
	}

	if flags.noWait {
		fmt.Printf("Subnet accepted (PENDING). Check status with:\n  dcctl subnet get --vnet %s %s\n", vnetIDStr, subnetID)
		return nil
	}

	final, err := pollSubnetUntilDone(ctx, tenantID, projectID, apiClient, vnetID, subnetID)
	if err != nil {
		return err
	}
	fmt.Println()
	printSubnet(final)
	return nil
}

func printSubnet(s *dcapi.Subnet) {
	fmt.Printf("  ID:      %s\n", s.Id)
	fmt.Printf("  VNet:    %s\n", s.VnetId)
	fmt.Printf("  Name:    %s\n", s.Name)
	fmt.Printf("  CIDR:    %s\n", s.Cidr)
	fmt.Printf("  Gateway: %s\n", cliutil.DerefOrDash(s.Gateway))
	fmt.Printf("  Status:  %s\n", s.Status)
}

func pollSubnetUntilDone(ctx context.Context, tenantID, projectID string, apiClient *client.Client, vnetID, subnetID uuid.UUID) (*dcapi.Subnet, error) {
	const (
		pollInterval = 2 * time.Second
		timeout      = 60 * time.Second
	)
	deadline := time.Now().Add(timeout)
	fmt.Print("Waiting for Subnet to become active")

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		fmt.Print(".")

		resp, err := apiClient.Typed.GetSubnetWithResponse(ctx, tenantID, projectID, vnetID, subnetID)
		if err != nil || resp.JSON200 == nil {
			continue
		}
		s := resp.JSON200
		switch s.Status {
		case dcapi.ResourceStatusACTIVE:
			fmt.Println(" done!")
			return s, nil
		case dcapi.ResourceStatusFAILED:
			fmt.Println()
			return nil, fmt.Errorf("Subnet provisioning failed: %s", cliutil.DerefOrDash(s.Message))
		}
	}

	fmt.Println()
	return nil, fmt.Errorf("timed out after %s — check status with: dcctl subnet get --vnet %s %s", timeout, vnetID, subnetID)
}
