package config

import (
	"fmt"
	"os"
)

// Config holds the operator's configuration. The operator is a
// controller-runtime manager whose only external state is what it needs to
// render and install the Harbor Helm chart.
type Config struct {
	Helm HelmConfig
}

// HelmConfig holds the settings used to render and install the Harbor chart.
type HelmConfig struct {
	HarborRepoURL  string
	HarborChartVer string
	StorageClass   string
	IngressClass   string
	CertIssuer     string
	// BaseDomain is the suffix every registry URL is built on, as
	// registry.<namespace>.<BaseDomain>. With nip.io it MUST use the
	// dash-separated form (192-168-10-6.nip.io): nip.io finds the address by
	// scanning for four dot-separated octets anywhere in the name, so a
	// namespace ending in a digit ("project-1") merges into the dotted form and
	// resolves to the wrong host.
	BaseDomain string
	// InsecureHarborTLS skips TLS verification when the operator calls the
	// Harbor REST API (dev / self-signed certs only). Default false.
	InsecureHarborTLS bool
}

// Load builds the operator configuration from environment variables.
func Load() (*Config, error) {
	return &Config{
		Helm: HelmConfig{
			HarborRepoURL:     envStr("HARBOR_HELM_REPO", "https://helm.goharbor.io"),
			HarborChartVer:    envStr("HARBOR_CHART_VERSION", "1.14.0"),
			StorageClass:      envStr("STORAGE_CLASS", "longhorn"),
			IngressClass:      envStr("INGRESS_CLASS", "nginx"),
			CertIssuer:        envStr("CERT_ISSUER", "letsencrypt-prod"),
			BaseDomain:        mustEnv("BASE_DOMAIN"),
			InsecureHarborTLS: envBool("HARBOR_INSECURE_TLS", false),
		},
	}, nil
}

// mustEnv returns the value of key, panicking if it is unset or empty.
func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %s is not set", key))
	}
	return v
}

// envStr returns the value of key, or def if it is unset or empty.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool returns true when key is "true" or "1", or def if key is unset.
func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "true" || v == "1"
	}
	return def
}
