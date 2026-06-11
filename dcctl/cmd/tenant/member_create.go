package tenant

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/spf13/cobra"
	"github.com/wso2/dcctl/cmd/internal/cliutil"
	"github.com/wso2/dcctl/internal/client"
	dcapi "github.com/wso2/dcctl/internal/client/generated"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

// legacyRoles are the retired v1 rank names. They are rejected with a hint
// instead of being silently mapped to a v2 role-definition key.
var legacyRoles = map[string]bool{"owner": true, "member": true, "viewer": true}

func newMemberCreateCmd() *cobra.Command {
	var role string
	var alias string

	cmd := &cobra.Command{
		Use:   "create <email-or-sub>",
		Short: "Grant a user a role on the tenant",
		Long: `Grant a user a role on the active (or specified) tenant.

The user is identified by email address (resolved to an identity by the
platform's directory) or, alternatively, by their raw OIDC subject ("sub").
An argument containing "@" is treated as an email; anything else is sent
as a sub.

If the email does not match exactly one user — or the deployment has no
directory provider configured — the API returns 422; in that case grant by
the raw sub instead.

--role takes a role-definition key (e.g. Owner, Contributor, Reader).
List the full catalog with: GET /v1/role-definitions. The v1 ranks
(owner/member/viewer) are no longer accepted.

Examples:
  dcctl tenant member create alice@example.com --role Contributor
  dcctl tenant member create alice@example.com --role Owner --alias "Alice P"
  dcctl tenant member create 01abc123-0000-0000-0000-user000000001 --role Reader
  dcctl tenant member create alice@example.com --role Reader --tenant other-tenant`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantFlag, _ := cmd.Root().PersistentFlags().GetString("tenant")
			tenantID, err := dcconfig.GetTenantID(tenantFlag)
			if err != nil {
				return err
			}
			return runMemberCreate(cmd.Context(), args[0], role, alias, tenantID)
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Role-definition key to grant (e.g. Owner, Contributor, Reader); no default")
	cmd.Flags().StringVar(&alias, "alias", "", "Optional display alias shown instead of the opaque sub; defaults to the email on email grants")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func runMemberCreate(ctx context.Context, principal, role, alias, tenantID string) error {
	if legacyRoles[role] {
		return fmt.Errorf("%q is a retired v1 role rank — pass a role-definition key instead (e.g. Owner, Contributor, Reader; full catalog: GET /v1/role-definitions)", role)
	}

	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}

	req := dcapi.CreateRoleAssignmentRequest{RoleDefinition: role}
	if strings.Contains(principal, "@") {
		email := openapi_types.Email(principal)
		req.UserEmail = &email
	} else {
		req.UserSub = &principal
	}
	if alias != "" {
		req.DisplayAlias = &alias
	}

	apiClient := client.New(creds.AccessToken)
	resp, err := apiClient.Typed.CreateTenantRoleAssignmentWithResponse(ctx, tenantID, req)
	if err != nil {
		return fmt.Errorf("POST /v1/tenants/%s/role-assignments: %w", tenantID, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	ra := resp.JSON201
	fmt.Printf("Granted %s to %s on tenant %s (principal %s).\n",
		ra.RoleDefinition, principal, tenantID, ra.PrincipalId)
	return nil
}
