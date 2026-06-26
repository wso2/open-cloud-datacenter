package config

import "testing"

// setRequiredEnv sets the minimum DCAPI_* variables marked required:"true" so
// config.Load() succeeds. The toggle tests then layer the AGENT_ROUTE_* vars on
// top. t.Setenv auto-restores every key at the end of the test.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	required := map[string]string{
		"DCAPI_DB_URL":                       "postgres://u:p@localhost:5432/db?sslmode=disable",
		"DCAPI_OIDC_ISSUER":                  "https://issuer.example.com",
		"DCAPI_OIDC_AUDIENCE":                "test-client",
		"DCAPI_HARVESTER_KUBECONFIG":         "ZHVtbXk=", // base64("dummy")
		"DCAPI_RANCHER_URL":                  "https://rancher.example.com",
		"DCAPI_RANCHER_TOKEN":                "token-xyz",
		"DCAPI_RANCHER_HARVESTER_CREDENTIAL": "cattle-global-data:cc-xxxxx",
		"DCAPI_VPC_EXTERNAL_BRIDGE":          "mgmt-br",
		"DCAPI_VPC_EXTERNAL_CIDR":            "192.168.10.0/24",
		"DCAPI_VPC_EXTERNAL_GATEWAY":         "192.168.10.254",
	}
	for k, v := range required {
		t.Setenv(k, v)
	}
}

// TestAgentRouteWrites_DefaultFalse asserts the write toggle defaults to false
// when the env var is unset — writes stay on the direct path until an operator
// opts in.
func TestAgentRouteWrites_DefaultFalse(t *testing.T) {
	setRequiredEnv(t)
	// Intentionally do NOT set DCAPI_AGENT_ROUTE_WRITES.

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AgentRouteWrites {
		t.Error("AgentRouteWrites = true by default, want false")
	}
}

// TestAgentRouteToggles_Independent asserts the read and write toggles are wired
// to distinct env vars and move independently — reads can be on while writes stay
// off, and vice versa.
func TestAgentRouteToggles_Independent(t *testing.T) {
	cases := []struct {
		name                  string
		reads, writes         string
		wantReads, wantWrites bool
	}{
		{"both off", "false", "false", false, false},
		{"reads on, writes off", "true", "false", true, false},
		{"reads off, writes on", "false", "true", false, true},
		{"both on", "true", "true", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("DCAPI_AGENT_ROUTE_READS", tc.reads)
			t.Setenv("DCAPI_AGENT_ROUTE_WRITES", tc.writes)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.AgentRouteReads != tc.wantReads {
				t.Errorf("AgentRouteReads = %v, want %v", cfg.AgentRouteReads, tc.wantReads)
			}
			if cfg.AgentRouteWrites != tc.wantWrites {
				t.Errorf("AgentRouteWrites = %v, want %v", cfg.AgentRouteWrites, tc.wantWrites)
			}
		})
	}
}

// TestZonesEnabled_DefaultFalse asserts the flag defaults to false (single-cluster
// mode) when DCAPI_ZONES_ENABLED is unset — byte-identical legacy behavior.
func TestZonesEnabled_DefaultFalse(t *testing.T) {
	setRequiredEnv(t)
	// Intentionally do NOT set DCAPI_ZONES_ENABLED.

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ZonesEnabled {
		t.Error("ZonesEnabled = true by default, want false")
	}
}

// TestZonesEnabled_True asserts the flag reads true when the env var is set.
func TestZonesEnabled_True(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DCAPI_ZONES_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.ZonesEnabled {
		t.Error("ZonesEnabled = false with DCAPI_ZONES_ENABLED=true, want true")
	}
}

// TestValidateZones_DisabledRequiresKubeconfig asserts single-cluster mode fails
// fast when the Harvester kubeconfig is empty.
func TestValidateZones_DisabledRequiresKubeconfig(t *testing.T) {
	setRequiredEnv(t)
	// Clear the kubeconfig AFTER setRequiredEnv so the empty value wins.
	t.Setenv("DCAPI_HARVESTER_KUBECONFIG", "")
	// Zones disabled is the default; make it explicit for clarity.
	t.Setenv("DCAPI_ZONES_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.ValidateZones(); err == nil {
		t.Error("ValidateZones() = nil with zones disabled + empty kubeconfig, want an error")
	}
}

// TestValidateZones_EnabledKubeconfigOptional asserts multi-zone mode accepts an
// empty kubeconfig — agents provide cluster access.
func TestValidateZones_EnabledKubeconfigOptional(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DCAPI_HARVESTER_KUBECONFIG", "")
	t.Setenv("DCAPI_ZONES_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.ValidateZones(); err != nil {
		t.Errorf("ValidateZones() = %v with zones enabled + empty kubeconfig, want nil", err)
	}
}

// TestValidateZones_EnabledKubeconfigIgnored asserts multi-zone mode does NOT
// refuse a set kubeconfig — it is simply ignored (main.go logs one INFO).
func TestValidateZones_EnabledKubeconfigIgnored(t *testing.T) {
	setRequiredEnv(t) // sets a non-empty DCAPI_HARVESTER_KUBECONFIG
	t.Setenv("DCAPI_ZONES_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.ValidateZones(); err != nil {
		t.Errorf("ValidateZones() = %v with zones enabled + set kubeconfig, want nil (ignored, not refused)", err)
	}
}
