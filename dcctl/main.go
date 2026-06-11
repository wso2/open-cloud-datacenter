// Command dcctl is the developer-facing CLI for the WSO2 Infrastructure Platform platform.
//
// Usage:
//   dcctl login                      -- authenticate with Asgardeo
//   dcctl create vm [flags]          -- provision a virtual machine
//   dcctl create cluster [flags]     -- provision an RKE2 cluster
//   dcctl get vm <id>                -- get VM status
//   dcctl get cluster <id>           -- get cluster status
//   dcctl delete vm <id>             -- delete a VM
//   dcctl kubeconfig <cluster-id>    -- download kubeconfig for a cluster
//
// Configuration:
//   Config file:   ~/.dcctl/config.yaml
//   Credentials:   ~/.dcctl/credentials.json (created by `dcctl login`)
//   Env overrides: DCCTL_DCAPI_URL, DCCTL_OIDC_ISSUER, etc.
package main

import "github.com/wso2/dcctl/cmd"

func main() {
	cmd.Execute()
}
