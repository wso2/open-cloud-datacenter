# VM Data Backups Guide — restic

This guide walks you through backing up a standalone Harvester VM's **application
data** with [`restic`](https://restic.net), using your own S3 bucket, and
restoring it into a fresh VM — in the same datacenter or a different one.
Everything here is self-service — no platform-admin action is required beyond the
one-time tenant space you were already given.

`restic` runs **inside the guest** and pushes **client-side-encrypted**,
deduplicated snapshots straight to your **own S3 bucket**. Nothing in the
backup/restore path touches the platform — the object store is the only thing
that crosses a datacenter boundary, which is what makes cross-DC recovery simple.

It is **file/directory-level**: you choose exactly which paths to back up, and you
can restore a whole snapshot, a subtree, or a **single file**.

| Protects | Restores | Use when |
|----------|----------|----------|
| A VM's files/directories (app data, config) | A whole snapshot, a subtree, or one file — into any VM | "we lost VM data" or "we need this VM's data in another DC" |

> This is the general-VM counterpart to the
> [Cluster Backups Guide](../../k8s-cluster/examples/BACKUPS.md) (etcd + Velero).
> Use that for RKE2 *clusters*; use this for plain VMs. For a **database's** data
> directory, prefer the database's native backup tooling, not restic.
>
> **Prerequisite:** a VM provisioned per the [`vm` module README](../README.md),
> your own Rancher-scoped Harvester kubeconfig, and AWS credentials able to create
> an S3 bucket and an IAM user.

---

## Table of Contents

- [1. Create the bucket, IAM user, and repository password](#1-create-the-bucket-iam-user-and-repository-password)
- [2. Add the Terraform (cloud-init restic install + timer)](#2-add-the-terraform-cloud-init-restic-install--timer)
- [3. Apply](#3-apply)
- [4. Verify](#4-verify)
- [5. Restore](#5-restore)
- [6. Gotchas](#6-gotchas)
- [Scheduling & Retention Reference](#scheduling--retention-reference)
- [Production Hardening](#production-hardening)

---

## 1. Create the bucket, IAM user, and repository password

> The AWS CLI is shown for reproducibility, but you can do all of this in the AWS
> Console instead — the steps map one-to-one: **S3 → Create bucket**, then enable
> **Block Public Access**, **Versioning**, and a **noncurrent-version + incomplete
> multipart-upload lifecycle rule**; **IAM → Users → create user**, attach the
> inline policy below, and create an access key.

Create a bucket in your own AWS account (use your datacenter's region):

```bash
REGION=us-east-2
BUCKET=my-team-vm-bkps

# us-east-1 is S3's default region and rejects a LocationConstraint; every other
# region requires one.
if [ "$REGION" = "us-east-1" ]; then
  aws s3api create-bucket --bucket "$BUCKET" --region "$REGION"
else
  aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
    --create-bucket-configuration LocationConstraint="$REGION"
fi
aws s3api put-public-access-block --bucket "$BUCKET" \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
aws s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
# Versioning keeps noncurrent copies of deleted/pruned backups; expire them so
# retention actually bounds total storage (tune NoncurrentDays to taste).
aws s3api put-bucket-lifecycle-configuration --bucket "$BUCKET" \
  --lifecycle-configuration '{"Rules":[{"ID":"expire-noncurrent","Status":"Enabled","Filter":{},"NoncurrentVersionExpiration":{"NoncurrentDays":30},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}]}'
```

Create an IAM user and attach this least-privilege policy. restic needs the
multipart-upload actions; note the two ARNs — bucket-level actions have **no**
`/*`, object-level actions do:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ResticBucketList",
      "Effect": "Allow",
      "Action": ["s3:ListBucket", "s3:GetBucketLocation"],
      "Resource": "arn:aws:s3:::my-team-vm-bkps"
    },
    {
      "Sid": "ResticObjects",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": "arn:aws:s3:::my-team-vm-bkps/*"
    }
  ]
}
```

Generate an access key pair for the user.

Finally, choose a **repository password** — the key restic uses to encrypt
everything client-side:

```bash
openssl rand -base64 32
```

> The repository password is unrecoverable. **If you lose it, every backup is
> permanently undecryptable** — there is no reset. Store it in a secrets manager
> alongside the IAM keys, not only on the VM.

## 2. Add the Terraform (cloud-init restic install + timer)

restic is configured entirely through the VM's **cloud-init**, passed to the `vm`
module's `user_data`. There is no separate backup server or Helm app.

**`variables.tf`** — add:

```hcl
variable "restic_s3_bucket" { type = string }
variable "restic_s3_region" { type = string }
variable "restic_s3_access_key" {
  type      = string
  sensitive = true
}
variable "restic_s3_secret_key" {
  type      = string
  sensitive = true
}
variable "restic_password" {
  type      = string
  sensitive = true
}
```

**`main.tf`** — define the in-guest env file, the backup script, and a one-shot
boot setup script as `locals`:

```hcl
locals {
  # Repo URL + credentials + password. Written 0600, root-only. This file is the
  # one thing you must NEVER include in a backup (it protects the repository).
  restic_env = <<-ENV
    export RESTIC_REPOSITORY="s3:s3.${var.restic_s3_region}.amazonaws.com/${var.restic_s3_bucket}/my-vm"
    export AWS_ACCESS_KEY_ID="${var.restic_s3_access_key}"
    export AWS_SECRET_ACCESS_KEY="${var.restic_s3_secret_key}"
    export RESTIC_PASSWORD="${var.restic_password}"
  ENV

  # Init the repo once (idempotent), snapshot the chosen paths, then prune.
  restic_backup = <<-SH
    #!/usr/bin/env bash
    set -euo pipefail
    source /etc/my-app/restic.env
    restic cat config >/dev/null 2>&1 || restic init
    restic backup --tag my-app /srv/appdata /etc/my-app/app.conf
    restic forget --prune --keep-daily 7 --keep-weekly 4
  SH

  # Install restic, enable the timer, and take the first backup at boot.
  restic_setup = <<-SH
    #!/usr/bin/env bash
    set -euxo pipefail
    export DEBIAN_FRONTEND=noninteractive
    apt-get update && apt-get install -y restic
    systemctl daemon-reload
    systemctl enable --now restic-backup.timer
    /usr/bin/bash /opt/restic-backup.sh
  SH
}
```

…then pass a cloud-init that base64-embeds those scripts and the systemd units
**inside your existing `module` call**:

```hcl
module "my_vm" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/vm?ref=terraform/v0.1.8"

  name         = "my-vm"
  namespace    = "my-namespace"
  image_name   = "images/ubuntu-22-04"
  network_name = "my-net-namespace/my-network"

  user_data = <<-YAML
    #cloud-config
    password: ${var.vm_password}
    ssh_pwauth: True
    packages: [qemu-guest-agent]
    write_files:
      - path: /etc/my-app/restic.env
        permissions: '0600'
        encoding: b64
        content: ${base64encode(local.restic_env)}
      - path: /opt/restic-backup.sh
        permissions: '0755'
        encoding: b64
        content: ${base64encode(local.restic_backup)}
      - path: /opt/restic-setup.sh
        permissions: '0755'
        encoding: b64
        content: ${base64encode(local.restic_setup)}
      - path: /etc/systemd/system/restic-backup.service
        permissions: '0644'
        content: |
          [Unit]
          Description=restic backup of app data
          After=network-online.target
          Wants=network-online.target
          [Service]
          Type=oneshot
          ExecStart=/usr/bin/bash /opt/restic-backup.sh
      - path: /etc/systemd/system/restic-backup.timer
        permissions: '0644'
        content: |
          [Unit]
          Description=Run restic backup daily
          [Timer]
          OnCalendar=*-*-* 21:00:00 UTC
          RandomizedDelaySec=300
          Persistent=true
          [Install]
          WantedBy=timers.target
    runcmd:
      - systemctl enable --now qemu-guest-agent.service
      - bash /opt/restic-setup.sh
  YAML

  # MAC-agnostic netplan: match the NIC by NAME, not MAC. A restored/rebuilt VM
  # gets a new MAC; a MAC-pinned netplan would leave it with no network.
  network_data = <<-EOT
    version: 2
    ethernets:
      all-eth:
        match:
          name: "en*"
        dhcp4: true
  EOT
}
```

**`terraform.tfvars`** — add `restic_s3_bucket = "my-team-vm-bkps"` and
`restic_s3_region = "<your-region>"`.
**`secret.tfvars`** — add `restic_s3_access_key` / `restic_s3_secret_key` /
`restic_password`.

> **Why base64.** Cloud-init is delivered through the Rancher proxy, which sits
> behind a WAF that blocks raw shell payloads (`curl | bash`, `apt`, `mkfs`) in
> `runcmd`. Base64-embedding every script in `write_files` (`encoding: b64`) hides
> it from the WAF; keep `runcmd` to `bash /opt/...`.

## 3. Apply

```bash
terraform plan  -var-file="secret.tfvars"
terraform apply -var-file="secret.tfvars"
```

`wait_for_lease` defaults let apply return before cloud-init finishes; give the VM
a couple of minutes to install restic and take the first backup.

## 4. Verify

SSH into the VM (or use `virtctl ssh` / `virtctl console` through the Rancher
proxy). restic reads the root-only env file, so use `sudo`:

```bash
sudo bash -c 'source /etc/my-app/restic.env
  restic snapshots          # >=1 snapshot with your paths and tag
  restic check'             # "no errors were found"

systemctl list-timers restic-backup.timer                       # future NEXT

aws s3 ls s3://my-team-vm-bkps/my-vm/ --region us-east-2         # objects present
```

Generate some new data, run `sudo bash /opt/restic-backup.sh` again, and confirm a
**second** snapshot appears — only the changed data is uploaded (deduplication).

## 5. Restore

### 5.1 Into a fresh VM (same datacenter)

The real test — recover on a VM that never held the data:

1. Provision a **fresh shell VM** (another `vm` module call) whose cloud-init
   installs restic and drops the **same** `restic.env`, but runs **no** backup
   timer.
2. Restore and verify:

```bash
sudo bash -c 'source /etc/my-app/restic.env
  restic restore latest --tag my-app --target /'   # restore to original paths
# then verify: file list / checksums / app-specific counts against the source
```

Use `--target /tmp/restore` to stage into a scratch directory first.

### 5.2 A single file or subtree (file-level)

You don't need the whole snapshot:

```bash
source /etc/my-app/restic.env

# one path out of the latest snapshot
restic restore latest --tag my-app --include /srv/appdata/important.dat --target /tmp/r

# stream a single file to stdout
restic dump latest /srv/appdata/important.dat > /tmp/important.dat

# browse a snapshot as a filesystem, copy what you need, then Ctrl-C to unmount
restic mount /mnt/restic
```

### 5.3 In a different datacenter (cross-DC DR)

The restore VM half is identical — only *where* you create it differs:

1. Have the DC team provision a tenant space (namespace + network + RBAC) in
   the DR datacenter, one time.
2. Provision a fresh shell VM there, install restic, and point
   `RESTIC_REPOSITORY` at the **same bucket** (cross-region read — mind egress), or
   at an **S3-replicated copy** in the DR region for local reads.
3. `restic restore latest --tag my-app --target /` and verify.

Because the repository is a self-contained, encrypted object store, **any** VM
with the repo URL + IAM keys + password can restore it — no backup server, no
shared storage.

## 6. Gotchas

- **Never back up `restic.env`.** It holds the AWS keys and the repository
  password; putting it in the repo defeats the encryption. Back up data + config
  only.
- **MAC-agnostic netplan is mandatory** (`match: name "en*"`). A restored/rebuilt
  VM gets a new MAC; a MAC-pinned netplan leaves it with no IP.
- **Keep the same restic version** on the source and every restore VM. Ubuntu's apt
  build lags upstream; for production, pin a recent static binary in cloud-init.
- **Cross-region reads cost egress.** For frequent or large DR reads, enable S3
  cross-region replication and restore from the local copy.

---

## Scheduling & Retention Reference

The timer frequency = your **RPO**; `restic forget --keep-*` = retention.

| Tier | Timer (`OnCalendar`, UTC) | Retention (`forget`) |
|------|---------------------------|----------------------|
| Daily | `*-*-* 21:00:00` | `--keep-daily 7 --keep-weekly 4` |
| 12-hourly | `*-*-* 09,21:00:00` | `--keep-daily 14` |
| Hourly | `*-*-* *:00:00` | `--keep-hourly 24 --keep-daily 7` |

- `OnCalendar` is **UTC**. Add `RandomizedDelaySec` and stagger VMs so a fleet
  doesn't push to S3 at once.
- restic only uploads changed data (content-defined-chunking dedup), so
  incrementals are cheap.
- With bucket versioning enabled, pruning a snapshot leaves a **noncurrent
  version**; the noncurrent-version lifecycle rule from §1 is what caps total
  storage — set `NoncurrentDays` to your grace window.

## Production Hardening

- Block Public Access on, default encryption (SSE-S3 or SSE-KMS) on, versioning on
  **with a noncurrent-version expiration lifecycle rule**, and a bucket policy
  denying non-TLS access. Consider S3 Object Lock for ransomware resilience.
- **One bucket + IAM user per team**, least privilege; never write to another
  team's or the platform's bucket.
- Store `RESTIC_PASSWORD` and the IAM keys in a secrets manager. Never commit
  `secret.tfvars` — add it to `.gitignore`.
- Rotate IAM keys periodically.
- Monitor for failed or missed backups — a silent failure is indistinguishable
  from having no backup.

## Related

- [VM module reference](../README.md)
- [Cluster Backups Guide — etcd & Velero](../../k8s-cluster/examples/BACKUPS.md)
