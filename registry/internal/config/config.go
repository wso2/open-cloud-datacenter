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

	// MetricsCertDir holds a serving certificate (tls.crt/tls.key) for the
	// metrics endpoint. Empty means the manager self-signs for localhost,
	// which only a scraper skipping verification can read; set it to make the
	// endpoint verifiable under its Service DNS name.
	MetricsCertDir string
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
	// dash-separated form (192-0-2-1.nip.io), because a namespace ending in a
	// digit merges into the dotted form and resolves to a different address:
	//
	//	registry.project-1.192.0.2.1.nip.io  ->  1.192.0.2   (wrong host)
	//	registry.project1.192.0.2.1.nip.io   ->  192.0.2.1
	//	registry.project-1.192-0-2-1.nip.io  ->  192.0.2.1
	//
	// Reproduce with `getent hosts <name>`.
	BaseDomain string
}

// Load builds the operator configuration from environment variables.
func Load() (*Config, error) {
	baseDomain, err := requireEnv("BASE_DOMAIN")
	if err != nil {
		return nil, err
	}

	return &Config{
		Helm: HelmConfig{
			HarborRepoURL:  envStr("HARBOR_HELM_REPO", "https://helm.goharbor.io"),
			HarborChartVer: envStr("HARBOR_CHART_VERSION", "1.19.2"),
			StorageClass:   envStr("STORAGE_CLASS", "longhorn"),
			IngressClass:   envStr("INGRESS_CLASS", "nginx"),
			CertIssuer:     envStr("CERT_ISSUER", "letsencrypt-prod"),
			BaseDomain:     baseDomain,
		},
		MetricsCertDir: envStr("METRICS_CERT_DIR", ""),
	}, nil
}

// requireEnv returns the value of key, or an error if it is unset or empty.
// Startup still stops on a missing value; returning it lets the caller report
// which one instead of unwinding a stack.
func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return v, nil
}

// envStr returns the value of key, or def if it is unset or empty.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
