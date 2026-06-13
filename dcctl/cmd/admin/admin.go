// Package admin holds the `dcctl admin` command tree — platform-admin-only
// operations that don't fit the tenant/project hierarchy:
//
//	dcctl admin tenant create <id> [--name --description --cpu-cores-cap ...]
//	dcctl admin tenant cap show <id>
//	dcctl admin tenant cap set   <id> [--cpu-cores --memory-gb --storage-gb]
//
// The hybrid quota model lives here: platform admin sets the tenant ceiling;
// tenant owners distribute that ceiling across projects via per-project
// quotas (managed via `dcctl project create/update`). See docs/defense-in-depth.md
// section 6 for the full model.
package admin

import (
	"github.com/spf13/cobra"
)

// NewAdminCmd returns the `dcctl admin` parent command.
func NewAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Platform-admin operations (tenant registration, cap management)",
		Long: `Commands that require platform-admin privileges (DCAPI_PLATFORM_ADMIN_SUBS
or DCAPI_ADMIN_GROUP membership).

Non-admin callers receive 403 from every endpoint under this group.`,
	}
	cmd.AddCommand(newAdminTenantCmd())
	cmd.AddCommand(newAdminAgentTokenCmd())
	return cmd
}

func newAdminTenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Manage tenants and their capacity caps",
	}
	cmd.AddCommand(
		newAdminTenantCreateCmd(),
		newAdminTenantCapCmd(),
	)
	return cmd
}
