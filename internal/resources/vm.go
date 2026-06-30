// Terraform resource definition for dcapi_virtual_machine.
//
// Key differences from VNet and Subnet:
//   1. Mutual-exclusion validation: the user must provide EITHER network_name (legacy mode)
//      OR (vnet_id + subnet_id) (VPC mode) — never both, never neither.
//   2. Shown-once secrets: private_key and console_password are only in the CREATE response.
//      Read must preserve them in state instead of overwriting with empty strings.
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"terraform-provider-dcapi/internal/client"
)

// ResourceVirtualMachine returns the *schema.Resource for "dcapi_virtual_machine".
func ResourceVirtualMachine() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — VMs have no update endpoint. Every field is ForceNew.
		CreateContext: resourceVMCreate,
		ReadContext:   resourceVMRead,
		DeleteContext: resourceVMDelete,

		Timeouts: &schema.ResourceTimeout{
			// VM provisioning (image pull + boot sequence) can take up to 10-15 minutes.
			Create: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Hostname label for this VM. Max 63 characters. Immutable.",
			},

			"size": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice(
					[]string{"small", "medium", "large", "xlarge"},
					false,
				),
				Description: "VM size: \"small\" (2vCPU/8GB), \"medium\" (4/16), \"large\" (8/32), \"xlarge\" (16/64). Immutable.",
			},

			"disk_gb": {
				Type:        schema.TypeInt,
				Optional:    true,
				ForceNew:    true,
				Description: "Boot disk size in GB (min 10). API uses size default when omitted. Immutable.",
			},

			"image_name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "VM image in \"namespace/resource-name\" format (e.g. \"rancher-infra/ubuntu-22-04\"). Immutable.",
			},

			// ── Networking: mutually exclusive modes ──
			// Provide EITHER network_name (legacy bridge) OR vnet_id + subnet_id (VPC). Never both.

			"network_name": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Legacy bridge-mode network ID (e.g. \"iaas/vm-network-001\"). Mutually exclusive with vnet_id and subnet_id. Immutable.",
			},

			"vnet_id": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "UUID of the VNet for VPC mode. Must pair with subnet_id. Mutually exclusive with network_name. Immutable.",
			},

			"subnet_id": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "UUID of the Subnet for VPC mode. Must pair with vnet_id. Mutually exclusive with network_name. Immutable.",
			},

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Used in the API URL path. Immutable.",
			},

			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent project. Used in the API URL path. Immutable.",
			},

			// ── Computed ──

			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED. Set by the API.",
			},

			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying hypervisor (e.g. \"harvester\"). Set by the API.",
			},

			"ip_address": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Assigned IP address. Empty until ACTIVE; populated by the API.",
			},

			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Useful for debugging FAILED state.",
			},

			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},

			// ── Computed + Sensitive (shown-once secrets) ──

			"private_key": {
				Type:      schema.TypeString,
				Computed:  true,
				Sensitive: true,
				Description: "SSH private key (PEM). SHOWN ONCE at creation — stored in state. " +
					"If state is lost, the VM must be deleted and recreated to obtain a new key.",
			},

			"console_password": {
				Type:      schema.TypeString,
				Computed:  true,
				Sensitive: true,
				Description: "Web-console password. SHOWN ONCE at creation — stored in state. " +
					"If state is lost, the VM must be deleted and recreated.",
			},
		},
	}
}

// resourceVMCreate creates a VM, immediately stores the one-time secrets, polls until
// ACTIVE, then calls Read to sync the final state.
func resourceVMCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	networkName := d.Get("network_name").(string)
	vnetID := d.Get("vnet_id").(string)
	subnetID := d.Get("subnet_id").(string)

	hasLegacy := networkName != ""
	hasVPC := vnetID != "" || subnetID != ""

	if hasLegacy && hasVPC {
		return diag.FromErr(fmt.Errorf(
			"invalid network configuration: provide EITHER network_name (legacy bridge mode) " +
				"OR vnet_id + subnet_id (VPC mode), not both",
		))
	}
	if !hasLegacy && !hasVPC {
		return diag.FromErr(fmt.Errorf(
			"invalid network configuration: provide EITHER network_name (legacy bridge mode) " +
				"OR vnet_id + subnet_id (VPC mode) — one networking mode is required",
		))
	}
	if hasVPC && (vnetID == "" || subnetID == "") {
		return diag.FromErr(fmt.Errorf(
			"invalid network configuration: VPC mode requires BOTH vnet_id and subnet_id — " +
				"provide both together or use network_name for legacy mode",
		))
	}

	req := client.VMCreateRequest{
		Name:        d.Get("name").(string),
		Size:        d.Get("size").(string),
		DiskGB:      d.Get("disk_gb").(int),
		ImageName:   d.Get("image_name").(string),
		NetworkName: networkName,
		VNetID:      vnetID,
		SubnetID:    subnetID,
	}

	resp, err := c.CreateVM(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating VirtualMachine: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, resp.Resource.ID))

	// Store shown-once secrets immediately — the API never returns them again.
	var diags diag.Diagnostics
	if err := d.Set("private_key", resp.PrivateKey); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("console_password", resp.ConsolePassword); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("status", resp.Resource.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", resp.Resource.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("ip_address", resp.Resource.IPAddress); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", resp.Resource.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", resp.Resource.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if diags.HasError() {
		return diags
	}

	if err := waitForVMActive(ctx, c, tenantID, projectID, resp.Resource.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	return resourceVMRead(ctx, d, meta)
}

// resourceVMRead fetches the current VM state from the API and updates Terraform state.
// It does NOT overwrite private_key or console_password — the GET response never includes them.
func resourceVMRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid VM state ID %q: expected 'tenant_id/project_id/vm_id'", d.Id()))
	}
	tenantID, projectID, vmID := parts[0], parts[1], parts[2]

	vm, err := c.GetVM(ctx, tenantID, projectID, vmID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading VirtualMachine %q: %w", vmID, err))
	}

	// nil, nil from GetVM means HTTP 404 — deleted outside Terraform.
	if vm == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	if err := d.Set("name", vm.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("size", vm.Size); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("status", vm.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", vm.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("ip_address", vm.IPAddress); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", vm.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", vm.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_id", tenantID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("project_id", projectID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	// Preserve shown-once secrets — the GET response never includes them.
	// Reading and writing back the existing state value keeps them unchanged.
	if err := d.Set("private_key", d.Get("private_key").(string)); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("console_password", d.Get("console_password").(string)); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	return diags
}

// resourceVMDelete initiates VM deletion and polls until the API confirms it is gone.
func resourceVMDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid VM state ID %q: expected 'tenant_id/project_id/vm_id'", d.Id()))
	}
	tenantID, projectID, vmID := parts[0], parts[1], parts[2]

	if err := c.DeleteVM(ctx, tenantID, projectID, vmID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting VirtualMachine %q: %w", vmID, err))
	}

	// Poll until HTTP 404 confirms the VM is fully cleaned up by the hypervisor.
	if err := waitForVMDeleted(ctx, c, tenantID, projectID, vmID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// waitForVMActive polls until the VM reaches status "ACTIVE" or timeout.
// VM provisioning (image pull + OS boot) is slower than VNet/Subnet provisioning —
// hence the 15-minute create timeout in ResourceVirtualMachine().
func waitForVMActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vmID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			vm, err := c.GetVM(ctx, tenantID, projectID, vmID)
			if err != nil {
				return nil, "", err
			}
			if vm == nil {
				return nil, "", fmt.Errorf("VirtualMachine %q disappeared while waiting for ACTIVE status", vmID)
			}
			if vm.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("VirtualMachine %q provisioning failed: %s", vmID, vm.Message)
			}
			return vm, vm.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}

// waitForVMDeleted polls until the VM is gone (HTTP 404) following an async DELETE.
func waitForVMDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vmID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		// "ACTIVE" is included because the GET may briefly return ACTIVE before the hypervisor accepts the shutdown.
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			vm, err := c.GetVM(ctx, tenantID, projectID, vmID)
			if err != nil {
				return nil, "", err
			}
			if vm == nil {
				return "deleted", "DELETED", nil
			}
			return vm, vm.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}
