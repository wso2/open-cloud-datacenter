# PostgreSQL Backups Guide — logical dumps to S3

This guide walks you through backing up a PostgreSQL server running on a
standalone Harvester VM with PostgreSQL's own tooling (`pg_dump` /
`pg_dumpall`), using your own S3 bucket, and restoring it into a fresh VM — in
the same datacenter or a different one. Everything here is self-service — no
platform-admin action is required beyond the one-time tenant space you were
already given.

The backup runs **inside the guest** as a timer, streams a **compressed logical
dump** of each database (plus the cluster's roles) straight to your **own S3
bucket**, and keeps a point-in-time history under a timestamped prefix. Nothing
in the backup/restore path touches the platform — the object store is the only
thing that crosses a datacenter boundary, which is what makes cross-DC recovery
simple.

It is **database-level**: you can restore every database, a **single database**,
or the roles alone, into any PostgreSQL VM of the same major version.

| Protects | Restores | Use when |
|----------|----------|----------|
| A PostgreSQL server's databases + roles/passwords | Every database, one database, or roles — into any PostgreSQL VM | "we lost the DB VM", "we dropped a database", or "we need this data in another DC" |

> This is the PostgreSQL counterpart to the
> [VM Data Backups Guide](./BACKUPS.md) (restic, for plain files) and the
> [Cluster Backups Guide](../../k8s-cluster/examples/BACKUPS.md) (etcd + Velero,
> for RKE2 clusters). Use **this** for a PostgreSQL database's data — logical
> dumps are portable across hosts and tolerant of same-or-newer restore targets,
> which restic snapshots of the data directory are not.
>
> **Prerequisite:** a VM provisioned per the [`vm` module README](../README.md)
> running **PostgreSQL** with local superuser access (the default `postgres` OS
> user via peer authentication), your own Rancher-scoped Harvester kubeconfig,
> and AWS credentials able to create an S3 bucket and an IAM user.

---

## Table of Contents

- [1. Create the bucket and IAM user](#1-create-the-bucket-and-iam-user)
- [2. Add the Terraform (cloud-init backup install + timer)](#2-add-the-terraform-cloud-init-backup-install--timer)
- [3. Apply](#3-apply)
- [4. Verify](#4-verify)
- [5. Restore](#5-restore)
- [6. Gotchas](#6-gotchas)
- [Scheduling & Retention Reference](#scheduling--retention-reference)
- [Production Hardening](#production-hardening)

---

## 1. Create the bucket and IAM user

> The AWS CLI is shown for reproducibility, but you can do all of this in the AWS
> Console instead — the steps map one-to-one: **S3 → Create bucket**, then enable
> **Block Public Access**, **Versioning**, and a **lifecycle rule** (object
> expiration + noncurrent-version + incomplete multipart-upload); **IAM → Users →
> create user**, attach the inline policy below, and create an access key.

Create a bucket in your own AWS account (use your datacenter's region):

```bash
REGION=us-east-2
BUCKET=my-team-db-bkps

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
# Expiration bounds your point-in-time history (retention); the noncurrent rule
# caps versioned copies; the multipart rule cleans up interrupted uploads. Tune
# the day counts to taste.
aws s3api put-bucket-lifecycle-configuration --bucket "$BUCKET" \
  --lifecycle-configuration '{"Rules":[{"ID":"db-backup-retention","Status":"Enabled","Filter":{},"Expiration":{"Days":30},"NoncurrentVersionExpiration":{"NoncurrentDays":7},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}]}'
```

Create an IAM user and attach this least-privilege policy. The multipart-upload
actions are required — large dumps upload in parts. Note the two ARNs —
bucket-level actions have **no** `/*`, object-level actions do:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "DbBackupBucketList",
      "Effect": "Allow",
      "Action": ["s3:ListBucket", "s3:GetBucketLocation"],
      "Resource": "arn:aws:s3:::my-team-db-bkps"
    },
    {
      "Sid": "DbBackupObjects",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": "arn:aws:s3:::my-team-db-bkps/*"
    }
  ]
}
```

Generate an access key pair for the user.

## 2. Add the Terraform (cloud-init backup install + timer)

The backup is configured entirely through the VM's **cloud-init**, passed to the
`vm` module's `user_data`. There is no separate backup server. The timer service
runs as the `postgres` user, so `pg_dump` authenticates locally by peer auth — no
database password is stored.

**`variables.tf`** — add:

```hcl
variable "db_s3_bucket" { type = string }
variable "db_s3_region" { type = string }
variable "db_s3_prefix" {
  type    = string
  default = "my-db" # per-VM prefix in the bucket; keep one prefix per server
}
variable "db_s3_access_key" {
  type      = string
  sensitive = true
}
variable "db_s3_secret_key" {
  type      = string
  sensitive = true
}
```

**`main.tf`** — define the in-guest env file, the backup script, and a one-shot
boot setup script as `locals`:

```hcl
locals {
  # S3 target + credentials. Written 0600, owned by postgres. NEVER include this
  # file in a backup — it holds the object-store keys.
  db_backup_env = <<-ENV
    DB_S3_BUCKET="${var.db_s3_bucket}"
    DB_S3_REGION="${var.db_s3_region}"
    DB_S3_PREFIX="${var.db_s3_prefix}"
    AWS_ACCESS_KEY_ID="${var.db_s3_access_key}"
    AWS_SECRET_ACCESS_KEY="${var.db_s3_secret_key}"
  ENV

  # Roles first (globals, incl. password hashes), then one compressed custom-format
  # dump per database — all streamed straight to a timestamped S3 prefix so no
  # local disk is needed. Retention is enforced by the S3 lifecycle rule (§1).
  db_backup = <<-SH
    #!/usr/bin/env bash
    set -euo pipefail
    source /etc/db-backup/backup.env
    TS=$(date -u +%Y%m%dT%H%M%SZ)
    DEST="s3://$DB_S3_BUCKET/$DB_S3_PREFIX/$TS"
    pg_dumpall --roles-only | aws s3 cp - "$DEST/roles.sql" --region "$DB_S3_REGION"
    for db in $(psql -Atc "SELECT datname FROM pg_database WHERE datistemplate=false AND datname<>'postgres';"); do
      pg_dump -Fc -d "$db" | aws s3 cp - "$DEST/$db.dump" --region "$DB_S3_REGION"
    done
  SH

  # Install the AWS CLI, enable the timer, and take the first backup at boot.
  db_backup_setup = <<-SH
    #!/usr/bin/env bash
    set -euxo pipefail
    export DEBIAN_FRONTEND=noninteractive
    apt-get update && apt-get install -y awscli
    systemctl daemon-reload
    systemctl enable --now db-backup.timer
    systemctl start db-backup.service
  SH
}
```

> **Security — where these secrets live.** The `db_backup_env` values (the AWS
> keys) are interpolated into `user_data`, which the module stores verbatim in a
> `harvester_cloudinit_secret` (a Kubernetes Secret in your namespace) and in your
> Terraform state. `sensitive = true` only redacts Terraform's console output — it
> does **not** encrypt state or the Secret. Keep read access to the Terraform state
> and the namespace's Secrets tightly limited, keep the IAM user least-privileged
> (§1) so any leak is bounded to this one bucket, and rotate the keys periodically.

…then pass a cloud-init that base64-embeds those scripts and the systemd units
**inside your existing `module` call**:

```hcl
module "my_db_vm" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/vm?ref=terraform/v0.1.8"

  name         = "my-db-vm"
  namespace    = "my-namespace"
  image_name   = "images/ubuntu-22-04"
  network_name = "my-net-namespace/my-network"

  user_data = <<-YAML
    #cloud-config
    password: ${jsonencode(var.vm_password)}
    ssh_pwauth: True
    packages: [qemu-guest-agent]
    write_files:
      - path: /etc/db-backup/backup.env
        permissions: '0600'
        owner: 'postgres:postgres'
        encoding: b64
        content: ${base64encode(local.db_backup_env)}
      - path: /opt/db-backup.sh
        permissions: '0755'
        encoding: b64
        content: ${base64encode(local.db_backup)}
      - path: /opt/db-backup-setup.sh
        permissions: '0755'
        encoding: b64
        content: ${base64encode(local.db_backup_setup)}
      - path: /etc/systemd/system/db-backup.service
        permissions: '0644'
        content: |
          [Unit]
          Description=PostgreSQL logical backup to S3
          After=network-online.target postgresql.service
          Wants=network-online.target
          [Service]
          Type=oneshot
          User=postgres
          ExecStart=/usr/bin/bash /opt/db-backup.sh
      - path: /etc/systemd/system/db-backup.timer
        permissions: '0644'
        content: |
          [Unit]
          Description=Run the PostgreSQL backup daily
          [Timer]
          OnCalendar=*-*-* 01:00:00 UTC
          RandomizedDelaySec=300
          Persistent=true
          [Install]
          WantedBy=timers.target
    runcmd:
      - systemctl enable --now qemu-guest-agent.service
      - bash /opt/db-backup-setup.sh
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

**`terraform.tfvars`** — add `db_s3_bucket = "my-team-db-bkps"`,
`db_s3_region = "<your-region>"`, and optionally `db_s3_prefix`.
**`secret.tfvars`** — add `db_s3_access_key` / `db_s3_secret_key`.

> **Why base64.** Cloud-init is delivered through the Rancher proxy, which sits
> behind a WAF that blocks raw shell payloads (`curl | bash`, `apt`, `pg_dump`) in
> `runcmd`. Base64-embedding every script in `write_files` (`encoding: b64`) hides
> it from the WAF; keep `runcmd` to `bash /opt/...`.

## 3. Apply

```bash
terraform plan  -var-file="secret.tfvars"
terraform apply -var-file="secret.tfvars"
```

The default wait behavior lets apply return before cloud-init finishes; give the
VM a couple of minutes to install the AWS CLI and take the first backup.

## 4. Verify

SSH into the VM (or use `virtctl ssh` / `virtctl console` through the Rancher
proxy) and confirm the timer and the objects in S3:

```bash
systemctl list-timers db-backup.timer                 # future NEXT
sudo journalctl -u db-backup.service --no-pager | tail # last run succeeded

# The dump landed under a timestamped prefix (env file is root/postgres-only):
sudo bash -c 'source /etc/db-backup/backup.env
  aws s3 ls "s3://$DB_S3_BUCKET/$DB_S3_PREFIX/" --region "$DB_S3_REGION"'
```

You should see a `roles.sql` and one `<db>.dump` per database under the latest
timestamp. Run `sudo systemctl start db-backup.service` again to take another
point-in-time backup on demand.

## 5. Restore

### 5.1 Into a fresh VM (same datacenter)

The real test — recover on a VM that never held the data:

1. Provision a **fresh PostgreSQL VM** (another `vm` module call) of the **same
   major version**, whose cloud-init installs the AWS CLI and drops the **same**
   `/etc/db-backup/backup.env`, but runs **no** backup timer.
2. Restore roles first, then each database. Streaming straight from S3 needs no
   local disk:

```bash
sudo -i -u postgres
source /etc/db-backup/backup.env

# pick the point in time to restore (latest shown here)
TS=$(aws s3 ls "s3://$DB_S3_BUCKET/$DB_S3_PREFIX/" --region "$DB_S3_REGION" \
      | awk '{print $2}' | tr -d / | sort | tail -1)
BASE="s3://$DB_S3_BUCKET/$DB_S3_PREFIX/$TS"

# 1. roles (benign "already exists" on built-in roles)
aws s3 cp "$BASE/roles.sql" - --region "$DB_S3_REGION" | psql -f -

# 2. every database — drop/recreate/restore, idempotent
for key in $(aws s3 ls "$BASE/" --region "$DB_S3_REGION" | awk '{print $4}' | grep '\.dump$'); do
  aws s3 cp "$BASE/$key" - --region "$DB_S3_REGION" \
    | pg_restore --clean --if-exists --create -d postgres
done
```

Because roles are restored with their password hashes, application connection
strings keep working unchanged.

### 5.2 A single database

You don't need the whole server — restore just one database from a chosen
timestamp:

```bash
sudo -i -u postgres
source /etc/db-backup/backup.env
TS=<timestamp>            # e.g. 20240115T010000Z, from `aws s3 ls`
aws s3 cp "s3://$DB_S3_BUCKET/$DB_S3_PREFIX/$TS/mydb.dump" - --region "$DB_S3_REGION" \
  | pg_restore --clean --if-exists --create -d postgres
```

If the database's owner role doesn't exist yet on this target, restore
`roles.sql` first (as in §5.1).

### 5.3 In a different datacenter (cross-DC DR)

The restore VM half is identical — only *where* you create it differs:

1. Have the DC team provision a tenant space (namespace + network + RBAC) in the
   DR datacenter, one time.
2. Provision a fresh PostgreSQL VM there, install the AWS CLI, and drop the same
   `backup.env` pointing at the **same bucket** (cross-region read — mind egress),
   or at an **S3-replicated copy** in the DR region for local reads.
3. Restore per §5.1 and verify.

To keep a **warm standby** (a running DR VM refreshed daily so failover is a
cutover, not a rebuild), give the DR VM the same restore commands on a timer that
fires a few hours **after** the source backup. At DR you then only repoint your
application/pipeline at the standby.

## 6. Gotchas

- **Never back up `backup.env`.** It holds the AWS keys; putting it in the bucket
  defeats the point. The scripts here dump databases only.
- **Match the major version.** Restore into the **same or a newer** PostgreSQL
  major version as the source; restoring into an older major version is not
  supported.
- **Restore roles before databases.** Databases are owned by roles, so the roles
  must exist first (§5.1 order). A lone `<db>.dump` restore assumes its owner role
  already exists.
- **MAC-agnostic netplan is mandatory** (`match: name "en*"`). A restored/rebuilt
  VM gets a new MAC; a MAC-pinned netplan leaves it with no IP.
- **The timer runs as `postgres`.** Keep `backup.env` owned `postgres:postgres`
  and mode `0600`, and ensure the `postgres` user can reach `aws` on `PATH`.

---

## Scheduling & Retention Reference

The timer frequency = your **RPO**; the S3 lifecycle `Expiration` (§1) = how far
back you can recover.

| Tier | Timer (`OnCalendar`, UTC) | Retention (S3 `Expiration` days) |
|------|---------------------------|----------------------------------|
| Daily | `*-*-* 01:00:00` | `30` |
| 12-hourly | `*-*-* 01,13:00:00` | `14` |
| Hourly | `*-*-* *:00:00` | `3` |

- `OnCalendar` is **UTC**. Add `RandomizedDelaySec` and stagger VMs so a fleet
  doesn't push to S3 at once.
- Each run writes a full, self-contained dump under a new timestamp — every
  timestamp is an independent restore point.
- Retention is the **S3 lifecycle rule**, not the script. With versioning on, the
  `Expiration` rule removes old current dumps and the `NoncurrentVersionExpiration`
  rule caps versioned copies — set both to your grace window.

## Production Hardening

- Block Public Access on, default encryption (SSE-S3 or SSE-KMS) on, versioning on
  **with expiration + noncurrent-version lifecycle rules**, and a bucket policy
  denying non-TLS access. Consider S3 Object Lock for ransomware resilience.
- **One bucket + IAM user per team**, least privilege; never write to another
  team's or the platform's bucket.
- Store the IAM keys in a secrets manager. Never commit `secret.tfvars` — add it
  to `.gitignore`.
- Rotate IAM keys periodically.
- Monitor for failed or missed backups — a silent failure is indistinguishable
  from having no backup. Alert on the `db-backup.service` result and on missing
  objects under today's prefix.

## Related

- [VM module reference](../README.md)
- [VM Data Backups Guide — restic](./BACKUPS.md)
- [MS-SQL Backups Guide — native BACKUP to S3](./MSSQL-BACKUPS.md)
- [Cluster Backups Guide — etcd & Velero](../../k8s-cluster/examples/BACKUPS.md)
