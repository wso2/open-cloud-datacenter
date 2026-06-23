package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Server     ServerConfig
	DB         DBConfig
	Auth       AuthConfig
	Kubernetes KubeConfig
	Helm       HelmConfig
	Encryption EncryptionConfig
}

type ServerConfig struct {
	Port            int
	ShutdownTimeout time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
}

type DBConfig struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type AuthConfig struct {
	JWKSEndpoint string
	Issuer       string
	Audience     string
}

type KubeConfig struct {
	// Own cluster (registry-controller-cluster) — where the provisioner pod itself runs.
	// The provisioner reads its own secrets/configmaps via mounted volumes, so in practice
	// it makes no API calls to its own cluster. Kept for future self-management use.
	InCluster      bool
	KubeconfigPath string // local dev fallback (KUBECONFIG env var)

	// Harvester cluster — the HCI cluster where all tenant management namespaces live.
	// Harbor (7 pods) is deployed here into each tenant's management namespace.
	// Loaded from a K8s Secret mounted as a file (HARVESTER_KUBECONFIG_PATH).
	HarvesterKubeconfigPath string
}

type HelmConfig struct {
	HarborRepoURL              string
	HarborChartVer             string
	StorageClass               string
	IngressClass               string
	CertIssuer                 string
	BaseDomain                 string
	IngressControllerNamespace string // namespace where ingress controller pods run
}

type EncryptionConfig struct {
	MasterKeyPath string
}

func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:            envInt("PORT", 8080),
			ShutdownTimeout: 30 * time.Second,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    60 * time.Second,
		},
		DB: DBConfig{
			DSN:             mustEnv("DATABASE_URL"),
			MaxOpenConns:    envInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    envInt("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: 5 * time.Minute,
		},
		Auth: AuthConfig{
			JWKSEndpoint: mustEnv("JWKS_ENDPOINT"),
			Issuer:       mustEnv("JWT_ISSUER"),
			Audience:     envStr("JWT_AUDIENCE", "registry-provisioner"),
		},
		Kubernetes: KubeConfig{
			InCluster:               envBool("KUBERNETES_IN_CLUSTER", true),
			KubeconfigPath:          envStr("KUBECONFIG", ""),
			HarvesterKubeconfigPath: envStr("HARVESTER_KUBECONFIG_PATH", ""), // empty = provisioner runs on Harvester itself (in-cluster)
		},
		Helm: HelmConfig{
			HarborRepoURL:              envStr("HARBOR_HELM_REPO", "https://helm.goharbor.io"),
			HarborChartVer:             envStr("HARBOR_CHART_VERSION", "1.14.0"),
			StorageClass:               envStr("STORAGE_CLASS", "longhorn"),
			IngressClass:               envStr("INGRESS_CLASS", "nginx"),
			CertIssuer:                 envStr("CERT_ISSUER", "letsencrypt-prod"),
			BaseDomain:                 mustEnv("BASE_DOMAIN"),
			IngressControllerNamespace: envStr("INGRESS_CONTROLLER_NAMESPACE", "kube-system"),
		},
		Encryption: EncryptionConfig{
			MasterKeyPath: envStr("MASTER_KEY_PATH", "/etc/secrets/master-key"),
		},
	}
	return cfg, nil
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

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}
