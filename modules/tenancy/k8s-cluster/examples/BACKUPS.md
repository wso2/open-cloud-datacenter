# Cluster Backups Guide — etcd Snapshots & Velero

This guide walks you through configuring backups for the RKE2 cluster you
provisioned with the [Cluster Provisioning Guide](./README.md), using your own
S3 buckets and your own Rancher API token. Everything here is self-service — no
platform-admin action is required.

There are **two independent layers**, and you want **both**:

| Layer | Protects | Restores | Use when |
|-------|----------|----------|----------|
| **etcd snapshots** | The cluster control plane (all Kubernetes API objects, RBAC, CRDs) | The whole cluster to a point in time | "the cluster/control plane broke" |
| **Velero** | Workloads inside namespaces + PersistentVolume data | A single namespace / resource / volume | "we lost or need to move an app" |

etcd cannot recover a deleted PVC's data; Velero cannot repair a corrupted
control plane. They are complementary.

> **Prerequisite:** a cluster provisioned per the [Cluster Provisioning Guide](./README.md),
> and the same Terraform directory + `secret.tfvars` you used there. You also need
> AWS credentials able to create an S3 bucket and an IAM user.

---

## Table of Contents

- [Part A — etcd S3 Snapshots](#part-a--etcd-s3-snapshots)
  1. [Create the bucket and IAM user](#a1-create-the-bucket-and-iam-user)
  2. [Add the Terraform](#a2-add-the-terraform)
  3. [Apply](#a3-apply)
  4. [Verify](#a4-verify)
  5. [Restore](#a5-restore)
  6. [Gotcha: manual snapshots cause drift](#a6-gotcha-manual-snapshots-cause-drift)
- [Part B — Velero](#part-b--velero)
  1. [Create the bucket and IAM user](#b1-create-the-bucket-and-iam-user)
  2. [Add the Terraform](#b2-add-the-terraform)
  3. [Apply](#b3-apply)
  4. [Verify](#b4-verify)
  5. [Back up and restore](#b5-back-up-and-restore)
- [Scheduling & Retention Reference](#scheduling--retention-reference)
- [Production Hardening](#production-hardening)

---

## Part A — etcd S3 Snapshots

etcd snapshots are a native RKE2 feature exposed by the `k8s-cluster` module's
`etcd_s3` input. They authenticate to S3 via a **Rancher S3 cloud credential** —
distinct from Velero (which uses raw keys in Helm values).

## A.1 Create the bucket and IAM user

Create a bucket in your own AWS account (use your datacenter's region):

```bash
REGION=us-east-2
BUCKET=my-team-cluster-bkps

aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
  --create-bucket-configuration LocationConstraint="$REGION"
aws s3api put-public-access-block --bucket "$BUCKET" \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
aws s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
```

Create an IAM user and attach this least-privilege policy (note the two ARNs —
bucket-level actions have **no** `/*`, object-level actions do):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EtcdSnapshotBucketList",
      "Effect": "Allow",
      "Action": ["s3:ListBucket", "s3:GetBucketLocation"],
      "Resource": "arn:aws:s3:::my-team-cluster-bkps"
    },
    {
      "Sid": "EtcdSnapshotObjects",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::my-team-cluster-bkps/*"
    }
  ]
}
```

> `DeleteObject` is required so RKE2 can prune snapshots beyond
> `snapshot_retention`. Without it, retention silently stops working.

Generate an access key pair for the user.

## A.2 Add the Terraform

**`variables.tf`** — add:

```hcl
variable "etcd_s3_bucket" { type = string }
variable "etcd_s3_region" {
  type    = string
  default = "us-east-2"
}
variable "etcd_s3_access_key" {
  type      = string
  sensitive = true
}
variable "etcd_s3_secret_key" {
  type      = string
  sensitive = true
}
```

**`main.tf`** — add a Rancher S3 cloud credential (the field names use the
`default_*` prefix required by the provider's `s3_credential_config` schema):

```hcl
resource "rancher2_cloud_credential" "etcd_s3" {
  name = "my-cluster-etcd-s3"
  s3_credential_config {
    access_key       = var.etcd_s3_access_key
    secret_key       = var.etcd_s3_secret_key
    default_region   = var.etcd_s3_region
    default_bucket   = var.etcd_s3_bucket
    default_endpoint = "s3.${var.etcd_s3_region}.amazonaws.com"
  }
}
```

…and add the `etcd_s3` block **inside your existing `module` call**:

```hcl
module "my_cluster" {
  # ... existing cluster config ...

  etcd_s3 = {
    bucket              = var.etcd_s3_bucket
    folder              = "my-cluster"            # per-cluster prefix in the bucket
    region              = var.etcd_s3_region
    cloud_credential_id = rancher2_cloud_credential.etcd_s3.id
    snapshot_retention  = 5                       # number of snapshots to keep
    snapshot_schedule   = "0 */6 * * *"           # 6-hourly (cron, UTC)
  }
}
```

**`terraform.tfvars`** — add `etcd_s3_bucket = "my-team-cluster-bkps"` (and region if not `us-east-2`).
**`secret.tfvars`** — add `etcd_s3_access_key` / `etcd_s3_secret_key`.

## A.3 Apply

```bash
terraform plan  -var-file="secret.tfvars"
terraform apply -var-file="secret.tfvars"
```

Adding `etcd_s3` updates `rke_config.etcd` **in place** — no node roll. If the
plan shows the cluster being **replaced/destroyed**, stop and investigate before
applying.

## A.4 Verify

**Rancher UI:** your cluster → **Snapshots** tab → **Snapshot Now** to trigger one
immediately; it should show `Successful` with an **S3** location.

**In S3:**

```bash
aws s3 ls s3://my-team-cluster-bkps/my-cluster/ --region us-east-2
# expect: <cluster>-etcd-snapshot-<node>-<timestamp> objects
```

After enough cycles, the object count equals `snapshot_retention` (older ones are
pruned).

## A.5 Restore

> An etcd restore rolls the **entire cluster** back to the snapshot's point in
> time — all API objects created afterward are lost. It is a control-plane
> recovery, not a per-app restore (use Velero for that). Rehearse on a throwaway
> cluster first.

Rancher UI → your cluster → **Snapshots** tab → select a snapshot → **Restore** →
choose "etcd only" or "Kubernetes version and etcd" → confirm.

## A.6 Gotcha: manual snapshots cause drift

Clicking **Snapshot Now** in the UI sets a one-shot trigger field
(`rke_config.etcd_snapshot_create`) on the cluster object. Terraform doesn't
manage that field, so the next `plan` will try to clear it. It is harmless (does
not delete snapshots or change the schedule), but to stop it recurring, add the
field to the module's `lifecycle` ignore list — take *on-demand* snapshots via
the UI, keep the *schedule* Terraform-managed.

---

## Part B — Velero

Velero backs up namespace-scoped Kubernetes resources and PersistentVolume data
(via Kopia file-system backup). It is installed as a Helm app onto your cluster.
You install it as the **cluster owner** (you created the cluster).

## B.1 Create the bucket and IAM user

Use a **separate bucket + IAM user** from etcd (least privilege):

```bash
REGION=us-east-2
BUCKET=my-team-velero-bkps

aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
  --create-bucket-configuration LocationConstraint="$REGION"
aws s3api put-public-access-block --bucket "$BUCKET" \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
aws s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled
```

IAM policy — note the extra multipart-upload actions Velero requires:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "VeleroBucketList",
      "Effect": "Allow",
      "Action": ["s3:ListBucket", "s3:GetBucketLocation"],
      "Resource": "arn:aws:s3:::my-team-velero-bkps"
    },
    {
      "Sid": "VeleroObjects",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": "arn:aws:s3:::my-team-velero-bkps/*"
    }
  ]
}
```

Generate an access key pair.

## B.2 Add the Terraform

**`variables.tf`** — add:

```hcl
variable "velero_s3_bucket" { type = string }
variable "velero_s3_region" {
  type    = string
  default = "us-east-2"
}
variable "velero_s3_access_key" {
  type      = string
  sensitive = true
}
variable "velero_s3_secret_key" {
  type      = string
  sensitive = true
}
```

**`backups.tf`** — register the Helm repo on your cluster and install Velero.
Replace `module.my_cluster.cluster_v3_id` with your cluster module's output, and
`my-cluster` with your cluster name; list the namespaces you want backed up:

```hcl
resource "rancher2_catalog_v2" "velero" {
  cluster_id = module.my_cluster.cluster_v3_id   # c-m-xxxxx, your downstream cluster
  name       = "vmware-tanzu"
  url        = "https://vmware-tanzu.github.io/helm-charts"
}

resource "rancher2_app_v2" "velero" {
  cluster_id    = module.my_cluster.cluster_v3_id
  name          = "velero"
  namespace     = "velero"
  repo_name     = rancher2_catalog_v2.velero.name
  chart_name    = "velero"
  chart_version = "8.1.0"          # pins a compatible Velero image — do NOT override image.tag
  wait          = true

  values = <<-YAML
    credentials:
      useSecret: true
      secretContents:
        cloud: |
          [default]
          aws_access_key_id=${var.velero_s3_access_key}
          aws_secret_access_key=${var.velero_s3_secret_key}

    configuration:
      backupStorageLocation:
        - name: default
          provider: aws
          bucket: ${var.velero_s3_bucket}
          prefix: my-cluster
          config:
            region: ${var.velero_s3_region}
      volumeSnapshotLocation:
        - name: default
          provider: aws
          config:
            region: ${var.velero_s3_region}
      defaultVolumesToFsBackup: true

    initContainers:
      - name: velero-plugin-for-aws
        image: velero/velero-plugin-for-aws:v1.9.0
        imagePullPolicy: IfNotPresent
        volumeMounts:
          - mountPath: /target
            name: plugins

    upgradeCRDs: false
    deployNodeAgent: true

    schedules:
      daily:
        disabled: false
        schedule: "0 21 * * *"        # cron, UTC — stagger to your window
        template:
          ttl: 168h                   # retention (7 days)
          includedNamespaces:
            - default
            - my-app
          includeClusterResources: true
          defaultVolumesToFsBackup: true
  YAML

  depends_on = [rancher2_catalog_v2.velero]
}
```

**`terraform.tfvars`** — add `velero_s3_bucket = "my-team-velero-bkps"` (+ region).
**`secret.tfvars`** — add `velero_s3_access_key` / `velero_s3_secret_key`.

## B.3 Apply

The Helm repo must be `Active` before the app installs, so apply in two steps:

```bash
terraform apply -var-file="secret.tfvars" -target=rancher2_catalog_v2.velero
# wait for the vmware-tanzu repo to show Active in Rancher → Apps → Repositories
terraform apply -var-file="secret.tfvars"
```

## B.4 Verify

Download your cluster's kubeconfig (Rancher → cluster → **Download KubeConfig**):

```bash
export KUBECONFIG=~/.kube/my-cluster.kubeconfig

kubectl get pods -n velero                                             # velero + node-agent Running
kubectl exec -n velero deploy/velero -- /velero backup-location get    # PHASE = Available
kubectl exec -n velero deploy/velero -- /velero schedule get           # your schedule, Enabled
```

> **Naming note:** the Helm chart prefixes schedule names with the release name.
> A schedule declared as `daily` is created as `velero-daily`. Use the prefixed
> name with `velero schedule describe` / `--from-schedule`.

## B.5 Back up and restore

Trigger a backup from the schedule (don't wait for the cron), then confirm S3:

```bash
kubectl exec -n velero deploy/velero -- /velero backup create --from-schedule velero-daily --wait
kubectl exec -n velero deploy/velero -- /velero backup get             # STATUS = Completed
aws s3 ls s3://my-team-velero-bkps/my-cluster/backups/ --region us-east-2
```

Restore a namespace (e.g. after accidental deletion):

```bash
kubectl exec -n velero deploy/velero -- /velero restore create \
  --from-backup <backup-name> --include-namespaces my-app --wait
kubectl exec -n velero deploy/velero -- /velero restore get
```

`PartiallyFailed` is common and usually benign — cluster-scoped resources (CRDs,
StorageClasses) that already exist are skipped. Verify pods/PVCs are healthy
before treating it as an error.

**Restoring to a different cluster (DR):** install Velero on the target pointing
at the same bucket + prefix, but set `accessMode: ReadOnly` on the
`backupStorageLocation` and configure **no** schedule, so the DR cluster can't
overwrite your source backups.

---

## Scheduling & Retention Reference

etcd snapshots are small and cheap; Velero backups move PV data and are heavier.
Frequency = your RPO; retention/TTL = how far back you can recover.

| | etcd (`snapshot_schedule` / `snapshot_retention`) | Velero (`schedule` / `ttl`) |
|---|---|---|
| Daily | `0 2 * * *` / `7` | `0 21 * * *` / `168h` |
| 6-hourly | `0 */6 * * *` / `12` | — |
| 12-hourly | — | `0 */12 * * *` / `336h` |

- Cron is **UTC**. Stagger schedules so clusters don't all back up at once.
- etcd `snapshot_retention` is a **count of snapshots**, not days.
- Velero `ttl` is when the backup is deleted from S3; back it with an S3
  lifecycle rule for cost control.

## Production Hardening

- Block Public Access on, default encryption (SSE-S3 or SSE-KMS) on, versioning
  on, and a bucket policy denying non-TLS access. Consider S3 Object Lock for
  ransomware resilience.
- **Separate buckets and IAM users** for etcd vs Velero.
- Never commit `secret.tfvars` — add it to `.gitignore`.
- Rotate IAM keys periodically.
- Monitor for failed/missed backups — a silent failure is indistinguishable from
  having no backup.

## Related

- [Cluster Provisioning Guide](./README.md)
- [`k8s-cluster` module reference](../README.md)
