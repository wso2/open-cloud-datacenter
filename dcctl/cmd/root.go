// Package cmd contains all dcctl Cobra commands.
//
// ── Why Cobra? ────────────────────────────────────────────────────────────────
//
// Cobra is the standard Go library for CLIs (used by kubectl, docker, helm, etc.).
// It gives us:
//   - Hierarchical commands: dcctl → vm → create
//   - Built-in help generation (--help on every command)
//   - Flag parsing with validation
//   - Shell completion (bash, zsh, fish, PowerShell)
//
// ── Command Tree (noun-chain — mirrors az / gcloud) ──────────────────────────
//
//   dcctl
//   ├── login / logout
//   ├── vm        create / get / list / delete
//   ├── cluster   create / get / list / delete / kubeconfig
//   ├── bastion   create / get / list / delete
//   ├── vnet      create / get / list / delete
//   ├── subnet    create / get / list / delete
//   ├── peering   create / get / list / delete
//   ├── keyvault  create / get / list / delete
//   │             └── endpoint  create / get / list / delete
//   ├── image     create / list
//   ├── network   list                         (legacy VM-bridge view)
//   ├── tenant    set / current / list / member (...) / service-account (...)
//   ├── project   create / list / get / update / delete / set / current
//   └── admin     tenant create / cap show / cap set / ...
//
// Each leaf command lives in its own file. The parent command (e.g. "vm")
// just groups subcommands — it has no action of its own.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/wso2/dcctl/cmd/admin"
	"github.com/wso2/dcctl/cmd/bastion"
	"github.com/wso2/dcctl/cmd/cluster"
	"github.com/wso2/dcctl/cmd/image"
	"github.com/wso2/dcctl/cmd/keyvault"
	"github.com/wso2/dcctl/cmd/network"
	"github.com/wso2/dcctl/cmd/peering"
	"github.com/wso2/dcctl/cmd/project"
	"github.com/wso2/dcctl/cmd/subnet"
	"github.com/wso2/dcctl/cmd/tenant"
	"github.com/wso2/dcctl/cmd/vm"
	"github.com/wso2/dcctl/cmd/vnet"
	"github.com/wso2/dcctl/internal/config"
)

// rootCmd is the top-level command. When you run `dcctl` with no sub-command,
// it prints the help message.
var rootCmd = &cobra.Command{
	Use:   "dcctl",
	Short: "WSO2 Infrastructure Platform CLI",
	Long: `dcctl — command-line interface for the WSO2 Datacenter Cloud Control Plane.

Run 'dcctl login' to authenticate, then choose a tenant and project:
  dcctl tenant list           — see all accessible tenants
  dcctl tenant set <id>       — set the active tenant (saved to ~/.dcctl/context.yaml)
  dcctl tenant current        — show the currently active tenant

  dcctl project list          — see all projects in the active tenant
  dcctl project set <id>      — set the active project for the current tenant
  dcctl project current       — show the currently active project

Tenant resolution order (first wins):
  1. --tenant <id> flag
  2. DCCTL_TENANT environment variable
  3. active_tenant in ~/.dcctl/context.yaml ('dcctl tenant set')
  4. tenant_id in ~/.dcctl/credentials.json (legacy login fallback)

Project resolution order (first wins):
  1. --project <id> flag
  2. DCCTL_PROJECT environment variable
  3. active_projects[<tenant>] in ~/.dcctl/context.yaml ('dcctl project set')

Resource commands follow the noun-chain pattern:
  dcctl <resource> <verb> [args]

  e.g.  dcctl vm create --name web-01 ...
        dcctl cluster list
        dcctl keyvault endpoint create <vault-id> --name foo ...`,
	// SilenceUsage: don't print the usage block on every error — only on --help.
	SilenceUsage: true,
}

// Execute is called from main.go. It parses args and runs the matched command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// outputFormat backs the global --output flag (currently unused below the
// parser, but kept for forward-compat with structured-output commands).
var outputFormat string

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags (inherited by all sub-commands)
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text",
		"Output format: text, json")
	rootCmd.PersistentFlags().String("dcapi-url", "",
		"DC-API base URL (overrides config file)")
	rootCmd.PersistentFlags().StringP("tenant", "t", "",
		"Tenant ID (defaults to the tenant stored in ~/.dcctl/credentials.json after login)")
	rootCmd.PersistentFlags().String("project", "",
		"Project ID to operate against (defaults to active project for the current tenant)")

	// Bind the --dcapi-url flag to viper so sub-commands can read it.
	viper.BindPFlag("dcapi_url", rootCmd.PersistentFlags().Lookup("dcapi-url")) //nolint:errcheck

	// Top-level utility commands
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newLogoutCmd())

	// Resource command trees (noun-chain).
	rootCmd.AddCommand(vm.NewVMCmd())
	rootCmd.AddCommand(cluster.NewClusterCmd())
	rootCmd.AddCommand(bastion.NewBastionCmd())
	rootCmd.AddCommand(vnet.NewVNetCmd())
	rootCmd.AddCommand(subnet.NewSubnetCmd())
	rootCmd.AddCommand(peering.NewPeeringCmd())
	rootCmd.AddCommand(keyvault.NewKeyVaultCmd())
	rootCmd.AddCommand(image.NewImageCmd())
	rootCmd.AddCommand(network.NewNetworkCmd())

	// Already-noun-chain command groups.
	rootCmd.AddCommand(tenant.NewTenantCmd())
	rootCmd.AddCommand(project.NewProjectCmd())
	rootCmd.AddCommand(admin.NewAdminCmd())
}

// initConfig loads the Viper configuration from ~/.dcctl/config.yaml.
// It is called once before any command runs.
func initConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: cannot determine home directory:", err)
		return
	}

	viper.AddConfigPath(home + "/.dcctl")
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")

	viper.SetEnvPrefix("DCCTL")
	viper.AutomaticEnv()

	defaults := config.Default()
	viper.SetDefault("dcapi_url", defaults.DCAPI)
	viper.SetDefault("oidc_issuer", defaults.OIDCIssuer)
	viper.SetDefault("client_id", defaults.ClientID)
	viper.SetDefault("callback_port", defaults.CallbackPort)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintln(os.Stderr, "warning: error reading config:", err)
		}
	}
}
