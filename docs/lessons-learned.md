# Lessons learned (don't make these again)

Project-specific traps we've already paid for. Re-reading these on a new session
is cheap; re-discovering them is not. `CLAUDE.md` keeps the operational guardrails
(the `sed -i` and `git commit -am` bans) inline; everything else lives here.

## The external uplink bridge is no longer safe for new bridge-mode workloads (post-F15)

**~10-minute outage (2026-05-11): the Harvester apiserver, Rancher UI, and
dc-api ingress all became unreachable from external clients when a tenant VM was
attached to a management bridge NAD via dcctl.**

What changed: F15 created a KubeOVN `ProviderNetwork` claiming the uplink bridge
as its external network. That bridge is now mediated by OVS flows (the
`localnet.ovn-vpc-external-network` logical switch port patches OVN's logical
network into the physical bridge) — it is no longer a plain Linux bridge.

Adding a NEW `type:bridge` NAD VM to the bridge triggers an OVS flow reconverge
and new kube-ovn-cni reactions. During that window, ARP for the kube-vip-served
VIPs on the LAN (the Harvester VIP, the secondary VIP, the dc-api ingress VIP)
gets dropped from upstream caches and isn't relearned until the offending VM
leaves the bridge.

Rules going forward:
- **Tenants always use VPCs** (`dcctl vm create --vnet … --subnet …`). F15's whole
  point is that tenant traffic stays on KubeOVN overlays.
- **Don't add new VMs to a management bridge NAD** (any bridge NAD that shares the
  uplink bridge). The only VM that legitimately lives there is the control-plane
  VM itself — it predates F15 and is a stable port-citizen.
- **Planned:** enforce this at the dc-api API layer — refuse VM-create on any
  bridge NAD whose underlying bridge has a KubeOVN ProviderNetwork attachment.
- **Cleaner long-term:** a dedicated VLAN for VPC external IPAM, separating it from
  the management broadcast domain entirely. After that, the uplink bridge could
  safely host new bridge VMs again (though tenants still shouldn't).

If a future session needs a tenant VM on a non-OVN network for testing, use a
different VLAN-tagged bridge NAD on a separate L2 domain — those are unaffected
by the uplink bridge's OVS mediation.

## Schema migrations — `schema.sql` is the single source

`internal/db/schema.sql` is fully idempotent — every statement is safe to run on
every dc-api boot. `internal/db/migrate.go` just `Exec`s the whole file on
startup; no sentinel, no alterations slice.

Pattern when adding new state:
- **New table:** `CREATE TABLE IF NOT EXISTS …` in `schema.sql`.
- **New column on an existing table:** append `ALTER TABLE … ADD COLUMN IF NOT
  EXISTS …` near the bottom (do NOT rewrite the original `CREATE TABLE` body —
  fresh-DB installs need the column AND upgrade-path DBs need the ALTER).
- **New enum value:** add to the `CREATE TYPE` body AND mirror as `ALTER TYPE …
  ADD VALUE IF NOT EXISTS` below.

The header comment of `schema.sql` lists all the idempotency idioms used.

## Never use `sed -i` to edit files on macOS — use the Edit tool

We have lost files **twice** to BSD `sed -i ''` silently truncating the target —
most recently when chaining three `sed -i '' s/foo/bar/g` calls in a single
shell invocation; one ~301-line file ended up at 0 bytes with no error and no
visible output. Recovered from git. The prior incident nuked a Go source file
the same way.

**Rule:** for any in-place modification of a tracked file, use the `Edit` tool
(or `Write` for full rewrites). Do not fall back to `sed -i`, `awk -i`,
`perl -pi`, or any in-place stream editor, even for a trivial one-line swap. The
failure mode is silent and destructive — by the time anyone notices, the file is
already gone. Multi-file replacements: a small loop of `Edit` calls is the right
pattern, even if it costs more tokens. Read-only `sed`/`awk` (piping to stdout,
generating env files) remains fine — the ban is specifically on `-i`-style edits.

## Git commit hygiene with parallel sub-agent work

Never use `git commit -am` (`-a` auto-stages all tracked modifications) when a
sub-agent may have written to the working tree — we have shipped commits that
swept up untested agent edits this way. Use `git add <specific files>` and review
the staged set before committing. If sub-agents are running in the background,
treat the working tree as contaminated until you've explicitly inventoried it.

## Sub-agent test claims must be verified

When a sub-agent reports "all clean" / "53/0/1 PASS" / "tests not run due to
permissions", treat it as **untested code** until you read the actual test output
yourself. We have shipped two bugs this way (a composite-auth chain with no
failing-test detection, plus VM-on-VPC code the agent literally said it couldn't
run tests on).

Pattern: between every meaningful chunk, run the integration suite yourself
against the live cluster, count PASS/FAIL/SKIP, verify it matches the expected
total (previous + new tests), and only then mark the chunk complete. A failing
test from "I'm sure it's fine" costs more than a 90-second suite run.

## KubeVirt bridge-mode VMs run a private DHCP server inside virt-launcher

**Caught 2026-05-12 during the F20 spike.** Spent ~45 min debugging why VMs on a
fresh VPC subnet were getting the K8s cluster DNS as their resolver even though
OVN's `DHCP_Options` table correctly advertised the per-VPC DNS pod IP. Root
cause: **KubeVirt's bridge-mode VMs have a built-in DHCP server in the
virt-launcher pod at link-local `169.254.75.10`** that races OVN's DHCP responder
and copies the pod's `/etc/resolv.conf` straight into the VM as DHCP option 6.
The default `dnsPolicy: ClusterFirst` gives the virt-launcher pod the cluster's
DNS + namespace search domains, so every VM ends up pointed at the cluster DNS
with `*.svc.cluster.local` search domains regardless of what the VPC's DHCP says.

The tcpdump that nailed it (from inside a freshly-DHCP'd VM):
```
169.254.0.254.67 > <vm>.68 (Offer): Domain-Name-Server: <vpc-dns-ip>   ← OVN
169.254.75.10.67 > <vm>.68 (ACK):   Domain-Name-Server: <cluster-dns>  ← KubeVirt
```

**Rule:** every VM dc-api creates on a VPC subnet **must** set both:
```yaml
spec.template.spec.dnsPolicy: None
spec.template.spec.dnsConfig.nameservers: [<vpc-dns-pod-ip>]
```
This is in `internal/providers/harvester/client.go::CreateVM` per F20. The
`dnsConfig` makes the virt-launcher pod's resolv.conf agree with OVN, so both
DHCP responders advertise the same DNS IP and the race becomes harmless. Applies
to ANY VM on a non-cluster network where the cluster DNS isn't reachable — the
symptom is "VM has DNS but can't resolve anything."

## Per-VPC infra pods pin tenant-subnet LSPs — subnet teardown order matters

**Caught 2026-05-12 from F20 integration test failures.** F15 NAT gateway pods
AND F20 CoreDNS pods both have NICs attached to the tenant subnet's logical
switch (via Multus + kubeovn-cni). Their LSPs (logical switch ports) pin the
subnet — KubeOVN's subnet delete won't complete until those LSPs are gone, which
won't happen until the pods are fully terminated.

Combined with the M2 contract "VNet delete requires no active subnets," this
creates a teardown-ordering trap:
1. Tenant deletes subnet → dc-api calls `provider.DeleteSubnet` → KubeOVN refuses
   (LSPs pinned).
2. Subnet stays in `DELETING` forever.
3. Tenant can't delete the VNet (subnet still active).
4. Resources stuck.

**The fix (shipped):** in `internal/api/handlers/subnet.go` DELETE handler, when
this is the LAST active subnet of the VPC, also tear down the per-VPC NAT gateway
and DNS Deployment first, then wait for the pods to actually drain (a
deterministic poll, NOT a fixed sleep), then call `provider.DeleteSubnet`.

The pattern generalizes: **any per-VPC pod with a NIC into a tenant subnet must be
cleaned up before that subnet can be deleted.** When adding new such pods, mirror
this teardown behavior or you'll re-introduce the race.

## Project namespace must be created via the provisioner, not just the DB row

**Caught 2026-05-21 during M2.5 stage-6 integration runs.**
`internal/db/projects.go::CreateProject` writes the projects row but does NOT
create the K8s namespace `dc-<tenant>-<project>`. The project handler calls
`ProjectNamespaceProvisioner.EnsureProjectNamespace(...)` after the DB insert to
actually create the namespace + ResourceQuota on the cluster. Test fixtures that
called the repo directly (`env.DB.CreateProject(...)`) skipped this step, causing
every subsequent VNet/Subnet/VM provisioner call to fail with
`namespaces "dc-<tenant>-<project>" not found`.

Rule: any code path that creates a project MUST also call `EnsureProjectNamespace`
(or go through `POST /v1/tenants/{tid}/projects`, which does it for you). Test
fixtures that bypass the HTTP layer now do this explicitly — see
`test/integration/fixtures.go::ensureDefaultProject`.

## Tenant-cap aggregate queries need every selected column in GROUP BY

PostgreSQL SQLSTATE 42803. `GetTenantCapAndAllocation` initially listed the tenant
cap columns in the SELECT but only `tenant_uuid` in GROUP BY; Postgres refused
because cap columns aren't aggregated and aren't functionally derivable from
`tenant_uuid` alone (the unique index on `tenant_uuid` doesn't make Postgres infer
functional dependency the way a primary key does in some grammars). Fix: add the
cap columns explicitly to GROUP BY. Generalises: when aggregating across a JOIN,
every non-aggregated column in SELECT must appear in GROUP BY.

## Project storage quota must account for CDI's 2×-disk-size prime PVC

**Caught 2026-05-21 during the M3 chunk-3 spike** when a bastion VM with a 40Gi
rootdisk stayed PENDING forever in a project with `storage_gb=50`.

Harvester's VM-image-clone path goes through CDI (Containerized Data Importer):
for each new disk, CDI creates a **prime PVC** of the same size to clone the image
into; once the image is imported the prime is deleted and its underlying Longhorn
volume becomes the real disk. During the import window the namespace's
`requests.storage` ResourceQuota sees `2 × disk size`. A 40Gi prime + a 40Gi real
PVC = 80Gi requested against a 50Gi cap → admission refuses the prime PVC with
`ErrCreatingPVCPrime`. The DataVolume stays Pending, no virt-launcher pod is ever
created, and dc-api stays in PENDING because the VM CR never reports Ready. The
symptom looks like a dc-api hang but the block is at the K8s admission layer.

**Rules going forward** (flagged, not all implemented):
- `db/projects.go::CreateProject` and `UpdateProject` should validate storage
  quota against a backstop derived from the largest catalog VM image:
  `quota ≥ 2 × max(image disk size in catalog)`. Hide the doubling from the user —
  they ask for "100Gi", we silently enforce ≥200Gi.
- The reconciler should detect `ErrCreatingPVCPrime` events on owned PVCs and flip
  the parent VM's status message to a user-actionable string ("project storage
  quota too low for image import; expand quota or reduce disk size") instead of
  leaving the row in unexplained PENDING.
- Long-term: investigate whether pre-imported Longhorn image templates let CDI
  skip the prime PVC entirely, eliminating the doubling.

## ARC runner queue gets orphaned after a listener restart

Symptom: `gh run list` shows N runs queued for an hour+; the ARC listener pod log
repeats `"assigned job"=0 decision=0 ... lastMessageID=<n>` forever even after
restart; ephemeral runner pods never appear. Workflow runs eventually time out as
"failure" with no log past the GitHub-side setup steps.

Cause: when the ARC listener pod restarts (crashloop, node reboot, pod eviction),
GitHub's broker still references the OLD session as the owner of the queued runs.
The new listener starts a fresh session and asks the broker for messages — the
broker returns zero, because the queued jobs are still pinned to the dead session.
They never get assigned and stay "queued" indefinitely.

Fix:
1. `gh run list --status queued --limit 20` — list the orphans.
2. `gh run cancel <id>` for each. Status flips to "cancelled".
3. `gh workflow run <yaml> --ref main` for each affected workflow. The fresh
   `workflow_dispatch` runs are assigned to the live listener session
   immediately; ephemeral runner pods come up within ~30 s.

Push-triggered queued runs never recover on their own — cancel + redispatch is the
only path. `workflow_dispatch` checks out HEAD of `--ref`, so the latest commit's
behaviour still lands.
