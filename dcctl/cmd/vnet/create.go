// Package vnet — VNet creation command.
package vnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

type vnetFlags struct {
	addressSpace string
	region       string
	outputJSON   bool
	noWait       bool
}

func newCreateCmd() *cobra.Command {
	flags := &vnetFlags{}

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a virtual network (VNet)",
		Long: `Create a VNet — the top-level network isolation boundary for tenant workloads.

VNets use KubeOVN under the hood but that detail is hidden from the caller.
Subnets and peerings are children of the VNet.

The address space must be an RFC1918 CIDR between /8 and /28 that does not
overlap with the platform's reserved ranges (checked server-side).

Example:
  # use active project
  dcctl vnet create vnet-a --address-space 10.10.0.0/16 --region lk
  # explicit project
  dcctl vnet create vnet-b --address-space 10.20.0.0/16 --region lk --tenant choreo-sre --project prod-infra
  dcctl vnet create vnet-c --address-space 10.30.0.0/16 --region lk --no-wait`,
		Args: cobra.ExactArgs(1),
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
			return runCreateVNet(cmd.Context(), tenantID, projectID, args[0], flags)
		},
	}

	cmd.Flags().StringVar(&flags.addressSpace, "address-space", "", "Address space CIDR (e.g. 10.10.0.0/16, required)")
	cmd.Flags().StringVar(&flags.region, "region", "", "Region name (e.g. lk, required)")
	cmd.Flags().BoolVar(&flags.outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "Return immediately without waiting for the VNet to become active")

	cmd.MarkFlagRequired("address-space") //nolint:errcheck
	cmd.MarkFlagRequired("region")        //nolint:errcheck

	return cmd
}

func runCreateVNet(ctx context.Context, tenantID, projectID, name string, flags *vnetFlags) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	fmt.Printf("Creating VNet %q...\n", name)
	resp, err := apiClient.Typed.CreateVNetWithResponse(ctx, tenantID, projectID, dcapi.CreateVNetRequest{
		Name:         name,
		AddressSpace: []string{flags.addressSpace},
		Region:       flags.region,
	})
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/vnets: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if flags.outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON202)
	}

	idStr, _ := resp.JSON202.Resource["id"].(string)
	if idStr == "" {
		fmt.Println("VNet accepted but no id in response — check 'dcctl vnet list'.")
		return nil
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return fmt.Errorf("server returned non-UUID id %q: %w", idStr, err)
	}

	if flags.noWait {
		fmt.Printf("VNet accepted (PENDING). Check status with:\n  dcctl vnet get %s\n", id)
		return nil
	}

	final, err := pollVNetUntilDone(ctx, tenantID, projectID, apiClient, id)
	if err != nil {
		return err
	}
	fmt.Println()
	printVNet(final)
	return nil
}

func printVNet(v *dcapi.VNet) {
	fmt.Printf("  ID:            %s\n", v.Id)
	fmt.Printf("  Name:          %s\n", v.Name)
	fmt.Printf("  Region:        %s\n", v.Region)
	fmt.Printf("  Address Space: %s\n", strings.Join(v.AddressSpace, ", "))
	fmt.Printf("  Status:        %s\n", v.Status)
}

func pollVNetUntilDone(ctx context.Context, tenantID, projectID string, apiClient *client.Client, id uuid.UUID) (*dcapi.VNet, error) {
	const (
		pollInterval = 2 * time.Second
		timeout      = 60 * time.Second
	)

	deadline := time.Now().Add(timeout)
	fmt.Print("Waiting for VNet to become active")

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		fmt.Print(".")

		resp, err := apiClient.Typed.GetVNetWithResponse(ctx, tenantID, projectID, id)
		if err != nil || resp.JSON200 == nil {
			continue
		}
		v := resp.JSON200
		switch v.Status {
		case dcapi.ResourceStatusACTIVE:
			fmt.Println(" done!")
			return v, nil
		case dcapi.ResourceStatusFAILED:
			fmt.Println()
			return nil, fmt.Errorf("VNet provisioning failed: %s", cliutil.DerefOrDash(v.Message))
		}
	}

	fmt.Println()
	return nil, fmt.Errorf("timed out after %s — check status with: dcctl vnet get %s", timeout, id)
}
