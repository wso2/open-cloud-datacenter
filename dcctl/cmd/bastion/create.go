package bastion

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

type bastionFlags struct {
	name        string
	vnet        string
	subnet      string
	description string
	saveKey     string
	outputJSON  bool
	noWait      bool
}

func newCreateCmd() *cobra.Command {
	flags := &bastionFlags{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an SSH bastion host on a VPC",
		Long: `Create a per-VPC SSH bastion host (F10).

A bastion is a small VM with two NICs:
  - mgmt NIC on an operator-reachable VLAN (so you can SSH in from your workstation)
  - OVN NIC on the target VPC's subnet (so you can ProxyJump to internal VMs)

Sizing and image are operator-controlled — you only choose the VPC.

Example:
  dcctl bastion create \
    --name prod-bastion \
    --vnet prod-vnet --subnet app-subnet \
    --save-key ~/.ssh/prod-bastion.pem

Use the resulting credentials to SSH in:
  ssh -i ~/.ssh/prod-bastion.pem -A ubuntu@<mgmt_ip>

Then ProxyJump to internal VMs (with the VM key loaded in your ssh-agent):
  ssh ubuntu@<vpc-vm-ip>`,
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
			return runCreateBastion(cmd.Context(), tenantID, projectID, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.name, "name", "n", "", "Bastion name (required)")
	cmd.Flags().StringVar(&flags.vnet, "vnet", "", "VNet name or ID (required)")
	cmd.Flags().StringVar(&flags.subnet, "subnet", "", "Subnet name or ID within the VNet (required)")
	cmd.Flags().StringVar(&flags.description, "description", "", "Optional free-text description")
	cmd.Flags().StringVar(&flags.saveKey, "save-key", "", "File path to write the generated SSH private key (e.g. ~/.ssh/bastion.pem)")
	cmd.Flags().BoolVar(&flags.outputJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "Return immediately without waiting for the bastion to become active")

	cmd.MarkFlagRequired("name")   //nolint:errcheck
	cmd.MarkFlagRequired("vnet")   //nolint:errcheck
	cmd.MarkFlagRequired("subnet") //nolint:errcheck

	return cmd
}

func runCreateBastion(ctx context.Context, tenantID, projectID string, flags *bastionFlags) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	vnetIDStr, err := apiClient.ResolveVNetID(tenantID, projectID, flags.vnet)
	if err != nil {
		return fmt.Errorf("resolve --vnet: %w", err)
	}
	subnetIDStr, err := apiClient.ResolveSubnetID(tenantID, projectID, vnetIDStr, flags.subnet)
	if err != nil {
		return fmt.Errorf("resolve --subnet: %w", err)
	}
	vnetID, err := uuid.Parse(vnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", vnetIDStr, err)
	}
	subnetID, err := uuid.Parse(subnetIDStr)
	if err != nil {
		return fmt.Errorf("invalid subnet id %q: %w", subnetIDStr, err)
	}

	body := dcapi.CreateBastionRequest{
		Name:     flags.name,
		VnetId:   vnetID,
		SubnetId: subnetID,
	}
	if flags.description != "" {
		body.Description = &flags.description
	}

	fmt.Printf("Creating bastion %q on vnet=%s subnet=%s...\n", flags.name, flags.vnet, flags.subnet)
	resp, err := apiClient.Typed.CreateBastionWithResponse(ctx, tenantID, projectID, body)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/bastions: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	created := resp.JSON202

	if flags.outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(created)
	}

	printBastionCredentials(flags, created.PrivateKey, created.ConsolePassword)

	if flags.noWait {
		fmt.Printf("\nBastion accepted (PENDING). Check status with:\n  dcctl bastion get %s\n", created.Resource.Id)
		return nil
	}

	final, err := pollBastionUntilDone(ctx, tenantID, projectID, apiClient, created.Resource.Id)
	if err != nil {
		return err
	}

	fmt.Println()
	mgmtIP := cliutil.DerefOrDash(final.MgmtIp)
	internalIP := cliutil.DerefOrDash(final.InternalIp)
	fmt.Printf("  ID:          %s\n", final.Id)
	fmt.Printf("  Name:        %s\n", final.Name)
	fmt.Printf("  Status:      %s\n", final.Status)
	fmt.Printf("  mgmt IP:     %s  (SSH to this address)\n", mgmtIP)
	fmt.Printf("  internal IP: %s  (the OVN-side address of the bastion in this VPC)\n", internalIP)

	if flags.saveKey != "" && mgmtIP != "-" {
		fmt.Printf("\n  Connect:\n    ssh -i %s -A ubuntu@%s\n", flags.saveKey, mgmtIP)
		fmt.Printf("  Then from inside the bastion, ProxyJump to internal VMs:\n")
		fmt.Printf("    ssh ubuntu@<vpc-vm-ip>\n")
	}
	return nil
}

func pollBastionUntilDone(ctx context.Context, tenantID, projectID string, apiClient *client.Client, id uuid.UUID) (*dcapi.Bastion, error) {
	const (
		pollInterval = 10 * time.Second
		timeout      = 10 * time.Minute
	)
	deadline := time.Now().Add(timeout)
	fmt.Print("Waiting for bastion to become active")

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		fmt.Print(".")

		resp, err := apiClient.Typed.GetBastionWithResponse(ctx, tenantID, projectID, id)
		if err != nil || resp.JSON200 == nil {
			continue
		}
		b := resp.JSON200
		switch b.Status {
		case dcapi.ResourceStatusACTIVE:
			if b.MgmtIp != nil && *b.MgmtIp != "" {
				fmt.Println(" done!")
				return b, nil
			}
		case dcapi.ResourceStatusFAILED:
			fmt.Println()
			return nil, fmt.Errorf("bastion provisioning failed: %s", cliutil.DerefOrDash(b.Message))
		}
	}

	fmt.Println()
	return nil, fmt.Errorf("timed out after %s — check status with: dcctl bastion get %s", timeout, id)
}

func printBastionCredentials(flags *bastionFlags, privateKey, consolePassword string) {
	if consolePassword != "" {
		fmt.Printf("\n  Console / SSH password: %s\n", consolePassword)
		fmt.Printf("  User: ubuntu\n")
	}
	if privateKey == "" {
		return
	}
	if flags.saveKey != "" {
		if err := os.WriteFile(flags.saveKey, []byte(privateKey), 0600); err != nil {
			fmt.Printf("\nWARNING: could not save private key to %s: %v\n", flags.saveKey, err)
			fmt.Printf("Private key (save this!):\n%s\n", privateKey)
		} else {
			fmt.Printf("\n  SSH private key saved to: %s\n", flags.saveKey)
		}
	} else {
		fmt.Printf("\nSSH Private Key (save this — will NOT be shown again):\n")
		fmt.Printf("─────────────────────────────────────────────────────────────\n")
		fmt.Print(privateKey)
		fmt.Printf("─────────────────────────────────────────────────────────────\n")
	}
}
