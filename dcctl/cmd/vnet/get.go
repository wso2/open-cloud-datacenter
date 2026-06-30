package vnet

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

func newGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <name-or-id>",
		Short: "Get a VNet",
		Args:  cobra.ExactArgs(1),
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
			return runGetVNet(cmd.Context(), tenantID, projectID, args[0], outputJSON)
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func runGetVNet(ctx context.Context, tenantID, projectID, idOrName string, outputJSON bool) error {
	creds, err := dcconfig.LoadCredentials()
	if err != nil {
		return err
	}
	apiClient := client.New(creds.AccessToken)

	idStr, err := apiClient.ResolveVNetID(tenantID, projectID, idOrName)
	if err != nil {
		return err
	}
	parsedID, err := uuid.Parse(idStr)
	if err != nil {
		return fmt.Errorf("invalid VNet id %q: %w", idStr, err)
	}

	resp, err := apiClient.Typed.GetVNetWithResponse(ctx, tenantID, projectID, parsedID)
	if err != nil {
		return fmt.Errorf("GET /v1/tenants/%s/vnets/%s: %w", tenantID, idStr, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return cliutil.APIErrorf(resp.StatusCode(), resp.Body)
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.JSON200)
	}

	printVNetDetail(resp.JSON200)
	return nil
}

func printVNetDetail(v *dcapi.VNet) {
	row(14, "ID", v.Id.String())
	row(14, "Name", v.Name)
	row(14, "Region", v.Region)
	row(14, "Zone", cliutil.Deref(v.Zone))
	row(14, "Status", string(v.Status))
	row(14, "Provider", v.ProviderType)
	row(14, "Tenant", v.TenantId)
	row(14, "Created", formatTime(v.CreatedAt))
	row(14, "Updated", formatTime(v.UpdatedAt))
	row(14, "Message", cliutil.Deref(v.Message))
	for i, cidr := range v.AddressSpace {
		label := "Address Space"
		if i > 0 {
			label = ""
		}
		row(14, label, cidr)
	}
}

func row(labelWidth int, label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("  %-*s %s\n", labelWidth, label+":", value)
}

func formatTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05Z07:00")
}
