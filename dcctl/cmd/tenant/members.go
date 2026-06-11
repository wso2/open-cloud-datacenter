// Package tenant provides the `dcctl tenant` sub-command group.
//
// Tenant scope selection:
//
//	dcctl tenant set <id>          — pin a tenant as the active default
//	dcctl tenant current           — print the currently active tenant
//	dcctl tenant list              — list all accessible tenants
//
// Member management (tenant-scope role assignments):
//
//	dcctl tenant member create <email-or-sub> --role <role-definition-key>
//	dcctl tenant member list
//	dcctl tenant member delete <user_sub>
//
// Service accounts:
//
//	dcctl tenant service-account create <name> --role <r>
//	dcctl tenant service-account list
//	dcctl tenant service-account delete <name-or-id>
package tenant

import "github.com/spf13/cobra"

// NewTenantCmd returns the `dcctl tenant` parent command.
func NewTenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "tenant",
		Short:        "Manage tenant selection, members, and service accounts",
		SilenceUsage: true,
	}

	// Scope commands (set, current) from context.go.
	for _, c := range NewContextCmds() {
		cmd.AddCommand(c)
	}

	// list (renamed from `dcctl list tenants`).
	cmd.AddCommand(newListCmd())

	// `dcctl tenant member <verb>` subgroup.
	memberCmd := &cobra.Command{
		Use:   "member",
		Short: "Manage human tenant members",
	}
	memberCmd.AddCommand(newMemberCreateCmd())
	memberCmd.AddCommand(newMemberListCmd())
	memberCmd.AddCommand(newMemberDeleteCmd())
	cmd.AddCommand(memberCmd)

	// `dcctl tenant service-account <verb>` subgroup.
	saCmd := &cobra.Command{
		Use:   "service-account",
		Short: "Manage tenant service accounts",
	}
	saCmd.AddCommand(newServiceAccountCreateCmd())
	saCmd.AddCommand(newServiceAccountListCmd())
	saCmd.AddCommand(newServiceAccountDeleteCmd())
	cmd.AddCommand(saCmd)

	return cmd
}
