package config

import (
	"strings"
	"testing"
)

func TestEnvStr(t *testing.T) {
	t.Run("returns the env var when set", func(t *testing.T) {
		t.Setenv("TEST_ENV_STR", "custom-value")
		if got := envStr("TEST_ENV_STR", "default-value"); got != "custom-value" {
			t.Errorf("envStr() = %q, want %q", got, "custom-value")
		}
	})
	t.Run("falls back to default when unset", func(t *testing.T) {
		if got := envStr("TEST_ENV_STR_NEVER_SET", "default-value"); got != "default-value" {
			t.Errorf("envStr() = %q, want %q", got, "default-value")
		}
	})
	t.Run("treats an explicitly empty value as unset", func(t *testing.T) {
		t.Setenv("TEST_ENV_STR_EMPTY", "")
		if got := envStr("TEST_ENV_STR_EMPTY", "default-value"); got != "default-value" {
			t.Errorf("envStr() = %q, want default %q for an explicitly empty env var", got, "default-value")
		}
	})
}

func TestRequireEnv(t *testing.T) {
	t.Run("returns the value when set", func(t *testing.T) {
		t.Setenv("TEST_REQUIRE_ENV", "required-value")
		got, err := requireEnv("TEST_REQUIRE_ENV")
		if err != nil {
			t.Fatalf("requireEnv() error = %v", err)
		}
		if got != "required-value" {
			t.Errorf("requireEnv() = %q, want %q", got, "required-value")
		}
	})
	t.Run("errors when unset, naming the variable", func(t *testing.T) {
		_, err := requireEnv("TEST_REQUIRE_ENV_NEVER_SET")
		if err == nil {
			t.Fatal("requireEnv() error = nil, want an error for a missing required env var")
		}
		if !strings.Contains(err.Error(), "TEST_REQUIRE_ENV_NEVER_SET") {
			t.Errorf("error = %v, want it to name the missing env var", err)
		}
	})
}

func TestLoadErrorsWithoutBaseDomain(t *testing.T) {
	t.Setenv("BASE_DOMAIN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error when BASE_DOMAIN is unset")
	}
}

func TestLoad(t *testing.T) {
	t.Run("required BASE_DOMAIN present, defaults fill the rest", func(t *testing.T) {
		t.Setenv("BASE_DOMAIN", "registry.example.com")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Helm.BaseDomain != "registry.example.com" {
			t.Errorf("Helm.BaseDomain = %q, want %q", cfg.Helm.BaseDomain, "registry.example.com")
		}
		if cfg.Helm.StorageClass != "longhorn" {
			t.Errorf("Helm.StorageClass default = %q, want %q", cfg.Helm.StorageClass, "longhorn")
		}
		if cfg.Helm.CertIssuer != "letsencrypt-prod" {
			t.Errorf("Helm.CertIssuer default = %q, want %q", cfg.Helm.CertIssuer, "letsencrypt-prod")
		}
	})

	t.Run("missing required BASE_DOMAIN errors rather than silently defaulting", func(t *testing.T) {
		t.Setenv("BASE_DOMAIN", "")
		cfg, err := Load()
		if err == nil {
			t.Fatal("Load() error = nil, want an error when BASE_DOMAIN is unset")
		}
		if cfg != nil {
			t.Errorf("Load() config = %v, want nil alongside the error", cfg)
		}
		if !strings.Contains(err.Error(), "BASE_DOMAIN") {
			t.Errorf("error = %v, want it to name BASE_DOMAIN", err)
		}
	})

	t.Run("env vars override every default", func(t *testing.T) {
		t.Setenv("BASE_DOMAIN", "registry.example.com")
		t.Setenv("STORAGE_CLASS", "custom-sc")
		t.Setenv("CERT_ISSUER", "selfsigned-issuer")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Helm.StorageClass != "custom-sc" {
			t.Errorf("Helm.StorageClass = %q, want override %q", cfg.Helm.StorageClass, "custom-sc")
		}
		if cfg.Helm.CertIssuer != "selfsigned-issuer" {
			t.Errorf("Helm.CertIssuer = %q, want override %q", cfg.Helm.CertIssuer, "selfsigned-issuer")
		}
	})
}
