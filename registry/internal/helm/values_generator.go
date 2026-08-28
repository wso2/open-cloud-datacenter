package helm

import (
	"bytes"
	"fmt"
	"text/template"
)

// SizePlan is the resource profile applied to one Harbor deployment.
type SizePlan struct {
	RegistryStorage string // e.g. "50Gi"
	DBStorage       string // e.g. "5Gi"
	CoreCPUReq      string
	CoreMemReq      string
	CoreCPULimit    string
	CoreMemLimit    string
	RegistryCPUReq  string
	RegistryMemReq  string
}

var plans = map[string]SizePlan{
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

// ValuesInput is the data rendered into the Harbor chart values template.
type ValuesInput struct {
	// Namespace is where this Harbor runs, and its identity in every rendered
	// name: the TLS Secret, the ingress host, and externalURL.
	Namespace    string
	BaseDomain   string
	StorageClass string
	IngressClass string
	CertIssuer   string
	Plan         SizePlan

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

// PlanFor returns the resource profile for a plan name.
func PlanFor(name string) (SizePlan, error) {
	p, ok := plans[name]
	if !ok {
		return SizePlan{}, fmt.Errorf("unknown plan %q; valid: starter, professional, enterprise", name)
	}
	return p, nil
}

// quote renders a value as a YAML double-quoted scalar, so a string
// containing a colon, newline, or quote cannot break the document structure
// or inject a key. Applied to every string field in the template.
func quote(s string) string {
	return fmt.Sprintf("%q", s)
}

var templateFuncs = template.FuncMap{"quote": quote}

// GenerateValues renders the Harbor chart values for one namespace's Harbor.
func GenerateValues(in ValuesInput) ([]byte, error) {
	tmpl, err := template.New("harbor-values").Funcs(templateFuncs).Parse(harborValuesTemplate)
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
      secretName: {{printf "%s-harbor-tls" .Namespace | quote}}
  ingress:
    hosts:
      core: {{printf "registry.%s.%s" .Namespace .BaseDomain | quote}}
    className: {{.IngressClass | quote}}
    annotations:
      kubernetes.io/ingress.class: {{.IngressClass | quote}}
      {{- if .CertIssuer}}
      cert-manager.io/cluster-issuer: {{.CertIssuer | quote}}
      {{- end}}
      nginx.ingress.kubernetes.io/proxy-body-size: "0"
      nginx.ingress.kubernetes.io/proxy-read-timeout: "900"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "900"

externalURL: {{printf "https://registry.%s.%s" .Namespace .BaseDomain | quote}}

# Read directly from our owned Secret (key: HARBOR_ADMIN_PASSWORD) instead of
# a literal value — keeps the admin password out of the Helm release record.
existingSecretAdminPassword: {{.SecretName | quote}}
existingSecretAdminPasswordKey: HARBOR_ADMIN_PASSWORD

# No existingSecret option exists for secretKey in the Harbor chart, so this
# stays a literal value. Still pinned (not left to chart auto-generation) so
# helm upgrade never rotates it.
secretKey: {{.EncryptionKey | quote}}

persistence:
  enabled: true
  resourcePolicy: keep
  persistentVolumeClaim:
    registry:
      storageClass: {{.StorageClass | quote}}
      size: {{.Plan.RegistryStorage | quote}}
      accessMode: ReadWriteOnce
    jobservice:
      jobLog:
        storageClass: {{.StorageClass | quote}}
        size: 1Gi
    database:
      storageClass: {{.StorageClass | quote}}
      size: {{.Plan.DBStorage | quote}}
    redis:
      storageClass: {{.StorageClass | quote}}
      size: 1Gi
    trivy:
      storageClass: {{.StorageClass | quote}}
      size: 5Gi

database:
  type: internal
  internal:
    # No existingSecret option for the internal database password in the
    # Harbor chart, so this stays a literal value — still our own random
    # value, not the chart's world-known "changeit" default.
    password: {{.DBPass | quote}}

redis:
  type: internal

core:
  # Read directly from our owned Secret instead of literal values — keeps
  # core.secret and core.xsrfKey out of the Helm release record.
  existingSecret: {{.SecretName | quote}}
  existingXsrfSecret: {{.SecretName | quote}}
  existingXsrfSecretKey: CSRF_KEY
  resources:
    requests:
      cpu: {{.Plan.CoreCPUReq | quote}}
      memory: {{.Plan.CoreMemReq | quote}}
    limits:
      cpu: {{.Plan.CoreCPULimit | quote}}
      memory: {{.Plan.CoreMemLimit | quote}}

registry:
  # Read directly from our owned Secret instead of a literal value.
  existingSecret: {{.SecretName | quote}}
  resources:
    requests:
      cpu: {{.Plan.RegistryCPUReq | quote}}
      memory: {{.Plan.RegistryMemReq | quote}}
    limits:
      cpu: 500m
      memory: 512Mi

jobservice:
  # Read directly from our owned Secret instead of a literal value.
  existingSecret: {{.SecretName | quote}}
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

logLevel: warning

# Run Harbor's schema migration as a Helm pre-upgrade Job instead of inside the
# starting core pod. Harbor's upstream upgrade guide is explicit that a version
# change migrates the schema and "the downtime cannot be avoid", and that
# "the database schema cannot be downgraded automatically, so the helm rollback
# is not supported".
#
# Given that, the choice is not whether to take an outage but whether it is
# defined. Left false (the chart default), migration runs inside the new core on
# startup while the OLD core is still serving through a RollingUpdate — so the
# previous version keeps answering requests against a schema changing underneath
# it. As a pre-upgrade hook the migration must instead complete before any pod
# rolls, and a failure aborts the upgrade rather than half-applying it.
enableMigrateHelmHook: true

notary:
  enabled: false
`
