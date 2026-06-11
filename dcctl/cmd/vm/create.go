// Package vm — VM creation command.
//
// `dcctl vm create` provisions a virtual machine by calling the DC-API.
//
// By default the command blocks and polls until the VM is ACTIVE (az-style).
// Pass --no-wait to return immediately after the API accepts the request.
package vm

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

type vmFlags struct {
	name        string
	size        string
	diskGB      int
	imageName   string
	networkName string
	vnet        string
	subnet      string
	outputJSON  bool
	saveKey     string
	noWait      bool
}

func newCreateCmd() *cobra.Command {
	flags := &vmFlags{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a virtual machine",
		Long: `Create a virtual machine in the datacenter.

DC-API generates an SSH key pair for you — you do not provide one.
The private key is printed once and never stored. Save it immediately.

  --save-key <path>  writes the private key to a file automatically
                     (e.g. --save-key ~/.ssh/web-01.pem)
                     chmod 0600 is applied automatically.

By default the command waits until the VM is running and prints the IP.
Pass --no-wait to return immediately and poll manually with:
  dcctl vm get <id>

Available sizes:  small (2 vCPU / 4 GiB)
                  medium (4 vCPU / 8 GiB)
                  large (8 vCPU / 16 GiB)
                  xlarge (16 vCPU / 32 GiB)
Use --disk to override the default disk size for that size.

Access methods after creation:
  SSH key:      ssh -i ~/.ssh/web-01.pem ubuntu@<ip>
  SSH password: ssh ubuntu@<ip>  (use the printed Console Password)

Example (legacy bridge network):
  dcctl vm create \
    --name web-01 \
    --size medium \
    --image default/image-rflb5 \
    --network default/vm-net-100 \
    --save-key ~/.ssh/web-01.pem

Example (M2 VNet/Subnet placement):
  dcctl vm create \
    --name vm-a \
    --size small \
    --image default/image-rflb5 \
    --vnet vnet-a --subnet sub-a \
    --save-key ~/.ssh/vm-a.pem`,
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
			return runCreateVM(cmd.Context(), tenantID, projectID, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.name, "name", "n", "", "VM name (required)")
	cmd.Flags().StringVarP(&flags.size, "size", "s", "", "VM size: small|medium|large|xlarge (required)")
	cmd.Flags().IntVarP(&flags.diskGB, "disk", "d", 0, "Root disk override in GiB (0 = use size default)")
	cmd.Flags().StringVarP(&flags.imageName, "image", "i", "", "OS image (namespace/name, required)")
	cmd.Flags().StringVar(&flags.networkName, "network", "", "Legacy bridge network (namespace/name). Mutually exclusive with --vnet/--subnet.")
	cmd.Flags().StringVar(&flags.vnet, "vnet", "", "M2 VNet name or ID for VM placement. Requires --subnet.")
	cmd.Flags().StringVar(&flags.subnet, "subnet", "", "M2 Subnet name or ID within the VNet. Requires --vnet.")
	cmd.Flags().BoolVar(&flags.outputJSON, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&flags.saveKey, "save-key", "", "File path to write the generated SSH private key (e.g. ~/.ssh/myvm.pem)")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "Return immediately without waiting for the VM to become active")

	cmd.MarkFlagRequired("name")  //nolint:errcheck
	cmd.MarkFlagRequired("size")  //nolint:errcheck
	cmd.MarkFlagRequired("image") //nolint:errcheck

	return cmd
}

func runCreateVM(ctx context.Context, tenantID, projectID string, flags *vmFlags) error {
	// Mutual exclusivity: --network and --vnet/--subnet are separate paths.
	if flags.networkName != "" && (flags.vnet != "" || flags.subnet != "") {
		return fmt.Errorf("--network and --vnet/--subnet are mutually exclusive; use one or the other")
	}
	if flags.networkName == "" && flags.vnet == "" {
		return fmt.Errorf("either --network (legacy) or both --vnet and --subnet (M2) are required")
	}
	if (flags.vnet == "") != (flags.subnet == "") {
		return fmt.Errorf("--vnet and --subnet must both be provided together")
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	body := dcapi.CreateVirtualMachineRequest{
		Name:      flags.name,
		Size:      dcapi.CreateVirtualMachineRequestSize(flags.size),
		ImageName: flags.imageName,
	}
	if flags.diskGB > 0 {
		body.DiskGb = &flags.diskGB
	}
	if flags.networkName != "" {
		body.NetworkName = &flags.networkName
	}
	if flags.vnet != "" {
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
		body.VnetId = &vnetID
		body.SubnetId = &subnetID
	}

	fmt.Printf("Creating VM %q...\n", flags.name)
	resp, err := apiClient.Typed.CreateVirtualMachineWithResponse(ctx, tenantID, projectID, body)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/virtual-machines: %w", tenantID, err)
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

	// Print credentials immediately — they are only returned once.
	printCredentials(flags, created.PrivateKey, created.ConsolePassword)

	if flags.noWait {
		fmt.Printf("\nVM accepted (PENDING). Check status with:\n  dcctl vm get %s\n", created.Resource.Id)
		return nil
	}

	// Poll until ACTIVE or FAILED (az-style default behaviour).
	final, err := pollUntilDone(ctx, tenantID, projectID, apiClient, created.Resource.Id)
	if err != nil {
		return err
	}

	fmt.Println()
	printVM(final)
	if keyPath := flags.saveKey; keyPath != "" {
		ip := cliutil.DerefOrDash(final.IpAddress)
		fmt.Printf("\n  Connect:  ssh -i %s ubuntu@%s\n", keyPath, ip)
		fmt.Printf("  Password: ssh ubuntu@%s\n", ip)
	}
	return nil
}

// pollUntilDone polls GET /v1/tenants/{tenant_id}/virtual-machines/{id} until
// status leaves PENDING. Prints a progress line that updates in place.
// Timeout: 10 minutes.
func pollUntilDone(ctx context.Context, tenantID, projectID string, apiClient *client.Client, id uuid.UUID) (*dcapi.VirtualMachine, error) {
	const (
		pollInterval = 10 * time.Second
		timeout      = 10 * time.Minute
	)

	deadline := time.Now().Add(timeout)
	fmt.Printf("Waiting for VM to become active")

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		fmt.Print(".")

		resp, err := apiClient.Typed.GetVirtualMachineWithResponse(ctx, tenantID, projectID, id)
		if err != nil || resp.JSON200 == nil {
			continue
		}
		vm := resp.JSON200
		switch vm.Status {
		case dcapi.ResourceStatusPENDING:
			continue
		case dcapi.ResourceStatusACTIVE:
			fmt.Println(" done!")
			return vm, nil
		case dcapi.ResourceStatusFAILED:
			fmt.Println()
			return nil, fmt.Errorf("VM provisioning failed: %s", cliutil.DerefOrDash(vm.Message))
		}
	}

	fmt.Println()
	return nil, fmt.Errorf("timed out after %s — check status with: dcctl vm get %s", timeout, id)
}

func printCredentials(flags *vmFlags, privateKey, consolePassword string) {
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
		fmt.Printf("Tip: use --save-key ~/.ssh/%s.pem to save automatically.\n", flags.name)
	}
}

func printVM(vm *dcapi.VirtualMachine) {
	size := "-"
	if vm.Size != nil {
		size = string(*vm.Size)
	}
	fmt.Printf("  ID:      %s\n", vm.Id)
	fmt.Printf("  Name:    %s\n", vm.Name)
	fmt.Printf("  Size:    %s\n", size)
	fmt.Printf("  Status:  %s\n", vm.Status)
	if ip := cliutil.DerefOrDash(vm.IpAddress); ip != "-" {
		fmt.Printf("  IP:      %s\n", ip)
	}
}
