package helm

import (
	"bytes"
	"fmt"
	"text/template"
)

type TenantPlan struct {
	RegistryStorage string // e.g. "50Gi"
	DBStorage       string // e.g. "5Gi"
	CoreCPUReq      string
	CoreMemReq      string
	CoreCPULimit    string
	CoreMemLimit    string
	RegistryCPUReq  string
	RegistryMemReq  string
}

var plans = map[string]TenantPlan{
	"starter": {
		RegistryStorage: "20Gi", DBStorage: "2Gi",
		CoreCPUReq: "100m", CoreMemReq: "128Mi", CoreCPULimit: "500m", CoreMemLimit: "512Mi",
		RegistryCPUReq: "50m", RegistryMemReq: "64Mi",
	},
	"professional": {
		RegistryStorage: "50Gi", DBStorage: "5Gi",
		CoreCPUReq: "200m", CoreMemReq: "256Mi", CoreCPULimit: "1000m", CoreMemLimit: "1Gi",
		RegistryCPUReq: "100m", RegistryMemReq: "128Mi",
	},
	"enterprise": {
		RegistryStorage: "200Gi", DBStorage: "10Gi",
		CoreCPUReq: "500m", CoreMemReq: "512Mi", CoreCPULimit: "2000m", CoreMemLimit: "2Gi",
		RegistryCPUReq: "200m", RegistryMemReq: "256Mi",
	},
}

type ValuesInput struct {
	TenantID     string
	BaseDomain   string
	StorageClass string
	IngressClass string
	CertIssuer   string
	Plan         TenantPlan

	// SecretName is the operator-owned Secret (built by ensureAdminSecret) that
	// Harbor's chart reads the admin password, core secret, xsrf key,
	// jobservice secret, and registry secret FROM DIRECTLY via its
	// existingSecret* fields — so those five values are never written into
	// values.yaml and never enter the Helm release record (visible via
	// `helm get values`/`helm get manifest`). The Secret must use the exact
	// data keys Harbor's chart requires: HARBOR_ADMIN_PASSWORD, secret,
	// CSRF_KEY, JOBSERVICE_SECRET, REGISTRY_HTTP_SECRET.
	SecretName string

	// DBPass and EncryptionKey have NO existingSecret equivalent anywhere in
	// the Harbor chart (verified against goharbor/harbor-helm values.yaml —
	// database.internal.password and the top-level secretKey are only ever
	// settable as literal values), so unlike the five secrets above they must
	// still be passed as raw template values. Both are pinned (not left for
	// the chart to auto-generate) so GenerateValues stays deterministic —
	// which is what makes upgrades safe — even though their plaintext still
	// ends up in the rendered values.yaml.
	DBPass        string // database.internal.password (16 chars)
	EncryptionKey string // secretKey — encrypts stored credentials (16 chars)
}

func PlanFor(name string) (TenantPlan, error) {
	p, ok := plans[name]
	if !ok {
		return TenantPlan{}, fmt.Errorf("unknown plan %q; valid: starter, professional, enterprise", name)
	}
	return p, nil
}

func GenerateValues(in ValuesInput) ([]byte, error) {
	tmpl, err := template.New("harbor-values").Parse(harborValuesTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, in); err != nil {
		return nil, fmt.Errorf("render template: %w", err)
	}
	return buf.Bytes(), nil
}

const harborValuesTemplate = `
expose:
  type: ingress
  tls:
    enabled: true
    certSource: secret
    secret:
      secretName: {{.TenantID}}-harbor-tls
  ingress:
    hosts:
      core: registry.{{.TenantID}}.{{.BaseDomain}}
    className: {{.IngressClass}}
    annotations:
      kubernetes.io/ingress.class: {{.IngressClass}}
      cert-manager.io/cluster-issuer: {{.CertIssuer}}
      nginx.ingress.kubernetes.io/proxy-body-size: "0"
      nginx.ingress.kubernetes.io/proxy-read-timeout: "900"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "900"

externalURL: https://registry.{{.TenantID}}.{{.BaseDomain}}

# Read directly from our owned Secret (key: HARBOR_ADMIN_PASSWORD) instead of
# a literal value — keeps the admin password out of the Helm release record.
existingSecretAdminPassword: "{{.SecretName}}"
existingSecretAdminPasswordKey: HARBOR_ADMIN_PASSWORD

# No existingSecret option exists for secretKey in the Harbor chart, so this
# stays a literal value. Still pinned (not left to chart auto-generation) so
# helm upgrade never rotates it.
secretKey: "{{.EncryptionKey}}"

persistence:
  enabled: true
  resourcePolicy: keep
  persistentVolumeClaim:
    registry:
      storageClass: {{.StorageClass}}
      size: {{.Plan.RegistryStorage}}
      accessMode: ReadWriteOnce
    jobservice:
      jobLog:
        storageClass: {{.StorageClass}}
        size: 1Gi
    database:
      storageClass: {{.StorageClass}}
      size: {{.Plan.DBStorage}}
    redis:
      storageClass: {{.StorageClass}}
      size: 1Gi
    trivy:
      storageClass: {{.StorageClass}}
      size: 5Gi

database:
  type: internal
  internal:
    # No existingSecret option for the internal database password in the
    # Harbor chart, so this stays a literal value — still our own random
    # value, not the chart's world-known "changeit" default.
    password: "{{.DBPass}}"

redis:
  type: internal

core:
  # Read directly from our owned Secret instead of literal values — keeps
  # core.secret and core.xsrfKey out of the Helm release record.
  existingSecret: "{{.SecretName}}"
  existingXsrfSecret: "{{.SecretName}}"
  existingXsrfSecretKey: CSRF_KEY
  resources:
    requests:
      cpu: {{.Plan.CoreCPUReq}}
      memory: {{.Plan.CoreMemReq}}
    limits:
      cpu: {{.Plan.CoreCPULimit}}
      memory: {{.Plan.CoreMemLimit}}

registry:
  # Read directly from our owned Secret instead of a literal value.
  existingSecret: "{{.SecretName}}"
  resources:
    requests:
      cpu: {{.Plan.RegistryCPUReq}}
      memory: {{.Plan.RegistryMemReq}}
    limits:
      cpu: 500m
      memory: 512Mi

jobservice:
  # Read directly from our owned Secret instead of a literal value.
  existingSecret: "{{.SecretName}}"
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 256Mi

portal:
  resources:
    requests:
      cpu: 10m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi

nginx:
  resources:
    requests:
      cpu: 10m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi

trivy:
  enabled: true
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 1000m
      memory: 2Gi

metrics:
  enabled: true
  serviceMonitor:
    enabled: false

updateStrategy:
  type: RollingUpdate

existingSecretAdminPassword: ""

logLevel: warning

notary:
  enabled: false
`
