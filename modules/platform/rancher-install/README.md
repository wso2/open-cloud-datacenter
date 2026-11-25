# Module: rancher-install

Installs Rancher onto an existing RKE2 cluster as a Helm release held in
**Terraform state** — hostname, replica count, chart version and every other
value are planned and diffed like any other resource, instead of being set once
by a cloud-init script nobody can re-run safely.

Pairs with [`platform/rancher-bootstrap`](../rancher-bootstrap/README.md), which
provides the cluster.

## Usage

```hcl
module "rancher_install" {
  source = "github.com/wso2/open-cloud-datacenter//modules/platform/rancher-install?ref=terraform/v0.3.0"

  rancher_hostname   = "rancher.example.com"
  bootstrap_password = var.bootstrap_password
  rancher_version    = "2.9.3"
  replicas           = 3

  # Bring your own certificate; no cert-manager needed.
  tls_source = "secret"
  tls_cert   = var.tls_cert
  tls_key    = var.tls_key

  control_plane_node_count = module.rancher_bootstrap.server_count
}
```

The caller configures the providers against the cluster's admin kubeconfig:

```hcl
provider "kubernetes" {
  config_path = var.kubeconfig_path
}

provider "helm" {
  # helm provider v3: kubernetes is an attribute, not a block.
  kubernetes = {
    config_path = var.kubeconfig_path
  }
}
```

## Chart channel

`use_rancher_prime` selects the repository and defaults to `false`:

| `use_rancher_prime` | Repository |
|---|---|
| `false` (default) | `https://releases.rancher.com/server-charts/latest` |
| `true` | `https://charts.rancher.com/server-charts/prime` |

`rancher_version` has no default and must be set — an unpinned chart means the
installed version depends on the day the apply ran.

## Values

Typed variables cover the values that normally get changed; everything else goes
through `additional_values`, which is passed to Helm as a **second values
document** rather than merged in Terraform. Helm merges values documents deeply,
so a nested override replaces only the leaf it names:

```hcl
additional_values = { ingress = { pathType = "Prefix" } }
# -> ingress.pathType is added; ingressClassName, extraAnnotations and
#    tls.source all survive.
```

Terraform's `merge()` is shallow and would have replaced the whole `ingress`
object, dropping the TLS source and leaving the chart to demand cert-manager.
Lists are still replaced wholesale (`extraEnv`, for instance) — that is how Helm
merges, not a module choice.

Two keys cannot be set this way, and a precondition rejects each:

- **`ingress.tls.source`** — the module keys secret creation and its own TLS
  preconditions off `tls_source`.
- **`hostname`** — the `rancher_url` and `rancher_hostname` outputs are built
  from `rancher_hostname`, downstream layers read them over remote state, and
  `tls_cert`/`tls_key` are issued for that name. An override would leave the
  outputs and the certificate pointing at a different host than the chart serves.

Use the typed `tls_source` and `rancher_hostname` variables instead.

The defaults render to:

```yaml
hostname: <rancher_hostname>
replicas: 3
ingress:
  ingressClassName: ""
  extraAnnotations: {}
  tls:
    source: secret
global:
  cattle:
    psp:
      enabled: false
extraEnv:
  - name: GODEBUG
    value: x509negativeserial=1
```

### Ingress class

`ingress_class_name` is blank by default, which leaves the chart value empty and
lets the Ingress be admitted by whichever IngressClass carries
`ingressclass.kubernetes.io/is-default-class`. That works, but it resolves at
admission time — install a second controller, or move that annotation, and the
Rancher Ingress is silently reassigned or left unclaimed. Name the class on any
cluster where that matters:

```hcl
ingress_class_name = "traefik"   # RKE2 with ingress-controller: ["traefik"]
ingress_class_name = "nginx"     # RKE2 still on the bundled ingress-nginx
```

### Ingress annotations

`ingress_extra_annotations` maps to the chart's `ingress.extraAnnotations`. The
chart renders ingress-nginx annotations only under
`includeDefaultExtraAnnotations`, deprecated since ingress-nginx's March 2026
end-of-life, so annotations for any other controller belong here.

**Traefik drops unsupported `nginx.ingress.kubernetes.io/*` annotations
silently** — no warning, no log line. `proxy-body-size` in particular is not
enforced; a 60 MB POST against a `50m` annotation reaches the backend. Test
functionally instead of assuming an annotation took effect, and prefer limits
that do not depend on the controller: Rancher's own `public-api-body-limit`
setting, or whatever sits in front of the cluster. If you do need an
ingress-level limit under Traefik, it is a `buffering` Middleware referenced
from here:

```hcl
ingress_extra_annotations = {
  "traefik.ingress.kubernetes.io/router.middlewares" = "cattle-system-body-limit@kubernetescrd"
}
```

`bootstrapPassword` is passed separately through `set_sensitive` so it stays out
of plan output. The chart only reads it on first install, so changing it later
does not rotate the live admin password — `ignore_changes` keeps re-applies
diff-clean rather than pretending otherwise.

## TLS

| `tls_source` | What happens | cert-manager |
|---|---|---|
| `secret` (default) | This module creates the `tls-rancher-ingress` secret from `tls_cert` / `tls_key` | not needed |
| `rancher` | Rancher issues a self-signed certificate | required |
| `letsEncrypt` | cert-manager solves an ACME challenge | required |

Set `install_cert_manager = true` for the latter two; a precondition on the
release fails the plan if cert-manager is missing when it is needed, or if
`tls_source = "secret"` without certificate material. `tls_ca_cert` populates the
`tls-ca` secret, needed only when the certificate is signed by a CA downstream
agents do not already trust.

### Private CA

Setting `tls_ca_cert` alongside `tls_source = "secret"` creates the `tls-ca`
secret **and** sets the chart's `privateCA = true`. Both are driven by one
local, because the chart only mounts `tls-ca` and omits `--no-cacerts` when
that flag is set — publishing the secret without it leaves the bundle unused
and Rancher clearing its CA certificates, so downstream agents never receive it.

Leave `tls_ca_cert` empty for a publicly trusted certificate: `privateCA` stays
`false` and the chart passes `--no-cacerts`, so Rancher serves no CA bundle.

**That is not sufficient on its own.** The chart's `agentTLSMode` defaults to
empty, which its own values file documents as meaning `strict` for new installs
on Rancher 2.9+. A strict agent verifies Rancher against `cacerts`, and
`--no-cacerts` leaves that empty — so downstream clusters can sit indefinitely
at *"Waiting for Agent to check in"* and `cattle-cluster-agent` crash-loops.
This module does not set `agentTLSMode`, so a public-certificate install needs
one of:

- **Public trust stores.** `additional_values = { agentTLSMode = "system-store" }`,
  which sets it at install time. Correct when the certificate chains to a
  public CA, as with Let's Encrypt or a commercial issuer.
- **Strict mode with a bundle.** Supply `tls_ca_cert`, which publishes `tls-ca`
  and flips `privateCA` to `true` so agents have a trust root to verify against.

The setting can also be changed after install through the Rancher
`agent-tls-mode` setting rather than the chart value — which is what
`environments/*/05-management` does in this repo, via
`rancher2_setting.agent_tls_mode`. Either route works; doing both means two
places disagree about the same value, so pick one.

**Rotating the bundle rolls the Rancher pods.** Changing `tls_ca_cert` between
two non-empty values updates the `tls-ca` secret but changes nothing else the
chart renders, so Helm would leave the Deployment alone and the running pods
would keep serving the old bundle — the chart mounts `cacerts.pem` with
`subPath`, and subPath mounts never pick up secret updates. The module therefore
adds `OCDC_TLS_CA_CHECKSUM` to `extraEnv`, carrying a sha256 of the bundle, so a
rotation changes the pod template and Helm rolls the Deployment. Only the digest
is in the values; the bundle itself stays in the secret.

Expect a rolling restart on any `tls_ca_cert` change.

## A note on `helm_release`

The rest of this catalog installs charts with the helm CLI through
`null_resource` + `local-exec`, because the Terraform helm provider cannot post
release Secrets through Rancher's downstream-cluster proxy ("request body too
large"). That constraint does not apply here — this module talks straight to the
RKE2 apiserver through the load balancer, with no Rancher proxy in the path.

## Key inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| rancher_hostname | FQDN Rancher is served on | `string` | n/a | yes |
| bootstrap_password | Initial admin password | `string` | n/a | yes |
| rancher_version | Chart version — must be pinned | `string` | n/a | yes |
| use_rancher_prime | Install from the Prime channel | `bool` | `false` | no |
| replicas | Rancher pod replicas | `number` | `3` | no |
| ingress_class_name | `ingress.ingressClassName`; `""` = cluster default | `string` | `""` | no |
| ingress_extra_annotations | `ingress.extraAnnotations` on the Rancher Ingress | `map(string)` | `{}` | no |
| tls_source | `secret`, `rancher` or `letsEncrypt` | `string` | `"secret"` | no |
| tls_cert / tls_key | Certificate material for `tls_source = "secret"` | `string` | `""` | no |
| tls_ca_cert | CA bundle published as the `tls-ca` secret; also sets the chart's `privateCA` | `string` | `""` | no |
| install_cert_manager | Install cert-manager alongside Rancher | `bool` | `false` | no |
| psp_enabled | `global.cattle.psp.enabled` | `bool` | `false` | no |
| extra_env | `extraEnv` list | `list(object)` | `GODEBUG=x509negativeserial=1` | no |
| additional_values | Extra chart values, merged last | `any` | `{}` | no |
| control_plane_node_count | Validates `replicas` against the cluster | `number` | `0` | no |

## Outputs

| Name | Description |
|------|-------------|
| rancher_url | `https://<rancher_hostname>` |
| rancher_hostname | FQDN Rancher is served on |
| chart_repository | Repository the release came from |
| rancher_version | Installed chart version |
| rancher_namespace | Namespace Rancher runs in |
| release_status | Helm release status |
