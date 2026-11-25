# Module: rancher-bootstrap

Brings up a multi-node RKE2 cluster as Harvester VMs — the substrate Rancher
runs on. It installs RKE2 and nothing else.

Rancher itself is installed by [`platform/rancher-install`](../rancher-install/README.md),
as a Helm release held in Terraform state, so the deployment is planned and
diffed rather than being a one-shot cloud-init script.

## Where this sits

| Phase | Module | Owns |
|---|---|---|
| 0a | [`platform/harvester-bootstrap`](../harvester-bootstrap/README.md) | Harvester admin password, admin kubeconfig |
| 0b | [`platform/harvester-config`](../harvester-config/README.md) | Namespace, networks, storage network, StorageClasses, OS image |
| 0c | **`platform/rancher-bootstrap`** | RKE2 node VMs |
| 0c | [`platform/nginx-lb-vm`](../nginx-lb-vm/README.md) | TCP load balancer in front of 6443 / 9345 / 443 / 80 |
| 0d | [`platform/rancher-install`](../rancher-install/README.md) | The Rancher Helm release |

## The load balancer comes first

Nodes join through the load balancer, and its address is baked into the
apiserver certificate, so `kube_api_host` must be decided **before** the first
node boots. Give `nginx-lb-vm` a static IP and pass that same IP here.

That ordering is why the LB is a separate module rather than an output of this
one: its IP is an input to the cluster, not a result of it.

## Usage

```hcl
module "rancher_bootstrap" {
  source = "github.com/wso2/open-cloud-datacenter//modules/platform/rancher-bootstrap?ref=terraform/v0.3.0"

  harvester_namespace = module.harvester_config.namespace
  image_id            = module.harvester_config.vm_image_id
  network_name        = module.harvester_config.vm_network_name

  # The nginx LB's static IP — join address and certificate SAN.
  kube_api_host  = "172.27.0.5"
  extra_tls_sans = ["rancher.example.com"]

  node_pools = [{
    name = "cp"
    ips  = ["172.27.0.10", "172.27.0.11", "172.27.0.12"]
  }]

  node_gateway       = "172.27.0.126"
  node_subnet_prefix = 25
  dns_servers        = ["8.8.8.8"]

  rke2_version = "v1.30.5+rke2r1"
  vm_password  = var.vm_password
}
```

## Node roles

`node_pools` is a list of pools, one VM per entry in `ips`. A node carrying
`control_plane` or `etcd` runs `rke2-server`; anything else joins as an agent.
All three role flags default to `true`, so the simple case above is three
servers. The first server bootstraps etcd (`cluster-init: true`) and the rest
join it through the load balancer.

`node_pools` validation fails the plan if no node carries `control_plane` or
`etcd`, or if an IP is repeated. An even number of servers only raises a
warning — etcd has no quorum advantage at an even count and loses the cluster on
a single failure, but a temporary even count during a resize is legitimate.

## Getting the kubeconfig

Each server node publishes an admin kubeconfig at `/home/ubuntu/.kube/config`
with the server address already rewritten to `https://<kube_api_host>:6443`, so
it works unchanged from outside the VM. Copy it off once the cluster is up:

```bash
terraform output -raw ssh_private_key > rke2_id_rsa && chmod 600 rke2_id_rsa
ssh -i rke2_id_rsa ubuntu@"$(terraform output -raw init_node_ip)" \
  sudo cat /home/ubuntu/.kube/config > rke2.kubeconfig
```

Store that file wherever the environment keeps its secrets; `rancher-install`
takes it as an input. Nothing in this module reads back from the cluster, so
there is no `local-exec`, no `null_resource` and no remote provisioner
anywhere in the path.

## RKE2 configuration

`/etc/rancher/rke2/config.yaml` is built in Terraform as a map, serialised with
`yamlencode()` and delivered through a cloud-init `write_files` entry — there
are no shell heredocs to mis-indent. Merge anything extra in with
`extra_server_config` / `extra_agent_config`:

```hcl
extra_server_config = {
  profile = "cis"
}
```

### Ingress controller

`ingress_controller` is written to the servers' `config.yaml` as RKE2's
`ingress-controller` key. The **first entry becomes the cluster's default
ingress class**.

```hcl
ingress_controller = ["traefik"]                     # default
ingress_controller = ["ingress-nginx", "traefik"]    # dual, nginx default
ingress_controller = ["none"]                        # bring your own
```

The default is `["traefik"]`. Ingress NGINX reached end-of-life in March 2026;
RKE2 v1.36 makes Traefik the default for new clusters and v1.37 drops
standalone `ingress-nginx` support.

Two caveats:

- **The setting only lands on first boot.** It travels via cloud-init, so
  changing it does not reconfigure running nodes — the VMs have to be rebuilt,
  or you follow RKE2's phased ingress migration out of band. A `check` warns
  when `rke2_version` predates the releases that understand the key
  (v1.32.11+, v1.33.7+, v1.34.3+, v1.35+).
- **Traefik does not honour every nginx annotation.** Anything carrying
  `nginx.ingress.kubernetes.io/*` — notably `proxy-body-size`, which Rancher
  installs need raised for large chart Secrets — has to be re-expressed as a
  Traefik Middleware.

### Bootstrap failures

The RKE2 install runs from `/usr/local/bin/rke2-bootstrap.sh`, written by
cloud-init and launched in the background. It logs to
`/var/log/rke2-bootstrap.log` and is **safe to re-run**:

```bash
sudo /usr/local/bin/rke2-bootstrap.sh
```

It returns immediately if the service is already active, retries the installer
download ten times, refuses to start a unit the installer did not produce, and
gives up on the kubeconfig wait after 900s rather than hanging forever. Every
failure path logs `FATAL:` and exits non-zero.

A node that never joins is therefore diagnosable from the log rather than from
a VM that looks healthy but is doing nothing.

### Node roles

Each pool's `control_plane` / `etcd` flags decide both whether a node runs
`rke2-server` and which server components it runs. RKE2 starts every component
unless disabled, so the flags are rendered as explicit disables:

| Pool roles | Rendered into `config.yaml` |
|---|---|
| `control_plane = true`, `etcd = true` | nothing extra — the full server |
| `control_plane = false`, `etcd = true` | `disable-apiserver`, `disable-controller-manager`, `disable-scheduler` |
| `control_plane = true`, `etcd = false` | `disable-etcd` |
| both `false` | joins as an agent |

`cluster-init` is placed on the first node carrying `etcd`, regardless of the
order the pools are declared in — it initialises the embedded datastore, so a
control-plane-only node cannot hold it.

`worker` is currently descriptive only: server nodes remain schedulable whether
or not it is set. Use `extra_server_config` with `node-taint` to keep workloads
off them.

### Changing a node's IP rebuilds that node

A static IP reaches the guest only through cloud-init, which renders network
configuration on an instance's first boot. Editing an address in `node_pools`
therefore **replaces that VM** — `replace_triggered_by` on a per-node
`terraform_data.node_address` makes this explicit rather than letting the secret
drift away from the running guest.

Only the affected node is replaced; the rest of the pool is untouched. On a
server node that still means an etcd member is destroyed and rejoined, so do it
one node at a time and let the cluster converge in between. Renumbering a whole
cluster is a rebuild, not an edit.

## Key inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| node_pools | Pools of nodes, one VM per IP | `list(object)` | n/a | yes |
| node_gateway | Default gateway for the node IPs | `string` | n/a | yes |
| kube_api_host | LB address: join target and certificate SAN | `string` | n/a | yes |
| image_id | Harvester image `<namespace>/<name>` | `string` | n/a | yes |
| network_name | VM network `<namespace>/<name>` | `string` | n/a | yes |
| rke2_version | Pinned RKE2 release, e.g. `v1.30.5+rke2r1` | `string` | n/a | yes |
| vm_password | Password for the `ubuntu` user | `string` | n/a | yes |
| harvester_namespace | Existing namespace (not created here) | `string` | `"default"` | no |
| node_subnet_prefix | CIDR prefix length of the node network | `number` | `24` | no |
| extra_tls_sans | Additional apiserver certificate SANs | `list(string)` | `[]` | no |
| ingress_controller | RKE2 ingress controllers; first is the default class | `list(string)` | `["traefik"]` | no |
| vm_cpu / vm_memory / vm_disk_size | Per-node hardware | | `8` / `16Gi` / `40Gi` | no |
| create_ssh_key | Generate and register an SSH key | `bool` | `true` | no |
| ssh_public_keys | OpenSSH public key lines installed for `ubuntu`; required when `create_ssh_key = false` | `list(string)` | `[]` | no |
| ssh_key_ids | Harvester SSH key IDs attached to the VM object — these do **not** reach the guest | `list(string)` | `[]` | no |

## Outputs

| Name | Description |
|------|-------------|
| node_ips | Every node IP — feed to `nginx-lb-vm` as `rke2_node_ips` |
| control_plane_ips | Control-plane node IPs |
| server_count | Number of rke2-server nodes (the natural Rancher replica count) |
| init_node_ip | Node that bootstrapped etcd — copy the kubeconfig from here |
| kube_api_url | `https://<kube_api_host>:6443` |
| vm_ids | Harvester resource IDs, keyed by node |
| ssh_key_id / ssh_public_key / ssh_private_key | SSH material (private key sensitive) |
| kubeconfig_fetch_command | The command above, with the init node filled in |
