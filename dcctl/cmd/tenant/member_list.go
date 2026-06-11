package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

func newMemberListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List user role assignments on the tenant",
		Long: `List all user role assignments on the active (or specified) tenant.

Service accounts are excluded; use "dcctl tenant service-account list".

Examples:
  dcctl tenant member list
  dcctl tenant member list --json
  dcctl tenant member list --tenant other-tenant`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runMemberList(cmd.Context(), tenantID, outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runMemberList(ctx context.Context, tenantID string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.ListTenantRoleAssignmentsWithResponse(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/role-assignments: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.RoleAssignments == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}
	assignments := *resp.JSON200.RoleAssignments

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(assignments)
	}

	if len(assignments) == 0 {
		fmt.Println("No members found.")
		return nil
	}

	fmt.Printf("%-40s  %-24s  %-28s  %-20s  %s\n",
		"PRINCIPAL_ID", "ALIAS", "ROLE", "GRANTED_AT", "GRANTED_BY")
	fmt.Printf("%-40s  %-24s  %-28s  %-20s  %s\n",
		strings.Repeat("-", 40), strings.Repeat("-", 24), strings.Repeat("-", 28),
		"--------------------", "--------------------")
	for _, ra := range assignments {
		fmt.Printf("%-40s  %-24s  %-28s  %-20s  %s\n",
			ra.PrincipalId,
			cliutil.Truncate(cliutil.DerefOrDash(ra.DisplayAlias), 24),
			ra.RoleDefinition,
			ra.GrantedAt.Format("2006-01-02 15:04:05"),
			ra.GrantedBy)
	}
	return nil
}
