package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wso2/dcctl/internal/auth"
	dcconfig "github.com/wso2/dcctl/internal/config"
)

// newLoginCmd returns the `dcctl login` command.
//
// This command runs the full OIDC Authorization Code + PKCE flow:
//  1. Start a local HTTP callback server on localhost:8085.
//  2. Open the Asgardeo login page in the user's browser.
//  3. Wait for the user to log in and Asgardeo to redirect back.
//  4. Exchange the authorization code for tokens.
//  5. Store tokens in ~/.dcctl/credentials.json.
//
// ── Why PKCE for a CLI? ───────────────────────────────────────────────────────
//
// A client secret in a CLI binary is NOT secret — any user can extract it with
// `strings` or a hex editor. PKCE replaces the secret with a per-login random
// challenge that cannot be reused. The CLI is a "public client" — no secret needed.
//
// ── No device flow? ──────────────────────────────────────────────────────────
//
// Device Authorization Grant (RFC 8628) is another option for headless/no-browser
// environments. We use Authorization Code + PKCE because Asgardeo supports it out of
// the box and it's more appropriate for developer workstations (which have browsers).
// Device flow can be added as an alternative for CI/CD pipelines.
func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Asgardeo (OIDC)",
		Long: `Log in to the WSO2 Infrastructure Platform using your Asgardeo account.

This command opens your default web browser to the Asgardeo login page.
After you authenticate, dcctl receives a token and stores it in:
  ~/.dcctl/credentials.json

Your credentials are automatically refreshed; you typically only need to
run 'dcctl login' once.`,
		RunE: runLogin,
	}
	return cmd
}

func runLogin(cmd *cobra.Command, args []string) error {
	issuer := viper.GetString("oidc_issuer")
	clientID := viper.GetString("client_id")
	callbackPort := viper.GetInt("callback_port")

	fmt.Printf("Authenticating with Asgardeo...\n")
	fmt.Printf("  Issuer:        %s\n", issuer)
	fmt.Printf("  Callback port: %d\n\n", callbackPort)

	// ── Step 1: Initialise the PKCE flow ─────────────────────────────────────
	// This performs OIDC discovery (fetches /.well-known/openid-configuration).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	flow, err := auth.NewPKCEFlow(ctx, issuer, clientID, callbackPort)
	if err != nil {
		return fmt.Errorf("initialise OIDC flow: %w", err)
	}

	// ── Step 2: Generate auth URL with PKCE challenge ─────────────────────────
	authURL, err := flow.AuthURL()
	if err != nil {
		return fmt.Errorf("build auth URL: %w", err)
	}

	// ── Step 3: Open browser ──────────────────────────────────────────────────
	fmt.Println("Opening browser for login...")
	fmt.Printf("If the browser does not open, visit this URL manually:\n  %s\n\n", authURL)

	if err := browser.OpenURL(authURL); err != nil {
		// Non-fatal: user can copy-paste the URL.
		fmt.Printf("(Could not open browser automatically: %v)\n\n", err)
	}

	// ── Step 4: Wait for the Asgardeo redirect callback ──────────────────────
	fmt.Printf("Waiting for login callback on http://localhost:%d/callback ...\n", callbackPort)
	code, err := flow.WaitForCallback(ctx)
	if err != nil {
		return fmt.Errorf("wait for login callback: %w", err)
	}

	// ── Step 5: Exchange authorization code for tokens ───────────────────────
	token, err := flow.ExchangeCode(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange auth code: %w", err)
	}

	// ── Step 6: Extract claims from id_token ─────────────────────────────────
	tenantID, sub, err := flow.ExtractClaims(ctx, token)
	if err != nil {
		return fmt.Errorf("extract token claims: %w", err)
	}

	// ── Step 7: Persist credentials ──────────────────────────────────────────
	creds := &dcconfig.Credentials{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		Expiry:       token.Expiry,
		TenantID:     tenantID,
		Sub:          sub,
	}
	if err := dcconfig.SaveCredentials(creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	fmt.Printf("\nLogin successful!\n")
	fmt.Printf("  User:          %s\n", sub)
	fmt.Printf("  Token expires: %s\n", token.Expiry.Format(time.RFC3339))

	// ── Step 8: Tenant context hint ───────────────────────────────────────────
	// If context.yaml already has an active tenant, say nothing — the user is
	// already set up. If not, auto-set when there is exactly one tenant, or
	// prompt the user to run 'dcctl tenant set'.
	activeCtx, _ := dcconfig.LoadContext()
	if activeCtx != nil && activeCtx.ActiveTenant != "" {
		fmt.Printf("  Active tenant: %s\n", activeCtx.ActiveTenant)
	} else if tenantID != "" {
		// Auto-set the single tenant returned from the JWT.
		newCtx := &dcconfig.Context{ActiveTenant: tenantID}
		if saveErr := dcconfig.SaveContext(newCtx); saveErr == nil {
			fmt.Printf("  Active tenant: %s (auto-set — single tenant)\n", tenantID)
		} else {
			// Non-fatal: just give the hint.
			fmt.Printf("  Active tenant: %s (from login — pin with 'dcctl tenant set %s')\n",
				tenantID, tenantID)
		}
	} else {
		fmt.Printf("\nRun 'dcctl tenant list' to see your tenants, then 'dcctl tenant set <id>' to choose one.\n")
	}

	fmt.Printf("\nRun 'dcctl create vm --help' to get started.\n")

	return nil
}
