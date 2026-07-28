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

type HelmConfig struct {
	HarborRepoURL  string
	HarborChartVer string
	StorageClass   string
	IngressClass   string
	CertIssuer     string
	BaseDomain     string
	// InsecureHarborTLS skips TLS verification when the operator calls the
	// Harbor REST API (dev / self-signed certs only). Default false.
	InsecureHarborTLS bool
}

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

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %s is not set", key))
	}
	return v
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "true" || v == "1"
	}
	return def
}
