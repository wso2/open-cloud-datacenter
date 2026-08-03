# Rancher Backup & Restore Configuration (S3-backed)

This runbook covers setting up the `rancher-backup` operator with S3 storage, encrypting backups at rest, creating one-time and recurring backups, and restoring Rancher from a backup.

## Prerequisites and notes

- The `rancher-backup` operator is installed only into the **local** cluster (the one running the Rancher server), and only backs up/restores the Rancher app itself — not downstream workload clusters. All backup/restore operations run against the local cluster.
- When restoring into a **new** Rancher setup, that setup must be the same Rancher version as the one the backup was taken from. Also check the Kubernetes version: the `apiVersion` supported by the cluster and by the backup file can differ across Kubernetes versions.
- Known Fleet issue: after a restore via the backup-restore operator, secrets used for `clientSecretName` and `helmSecretName` are not included in Fleet gitrepos — see Rancher's docs for the workaround.

## Constraints

**Scope**
- The operator only ever backs up/restores resources on the **local** cluster (wherever it's installed). Downstream clusters are never contacted or backed up directly — only their representation as objects on the local cluster (e.g. `clusters.provisioning.cattle.io` records) is captured.
- Deleting a downstream cluster and then "restoring" its local-cluster object back does **not** reprovision the actual cluster. This is explicitly unsupported and can leave Rancher, Fleet, and `rancher-webhook` in a broken state — don't attempt it.

**What actually gets backed up**
- Only resources matched by the chart's `resourceSet` are captured. User-created secrets are **not** backed up by default unless they carry the label `resources.cattle.io/backup: true`, or happen to already be matched by the default `resourceSet`.
- The `EncryptionConfiguration` file/key is never backed up by the operator itself — you must keep it (see step 4) and reuse the exact same file when restoring.
- If you need to change what's captured, edit the `resourceSet` **before** installing the chart — it's baked in at install time.

**Version matching (hard requirements)**
- The Rancher version you restore into must match the Rancher version the backup was taken from.
- Kubernetes version matters too — restoring resources whose `apiVersion` has been deprecated/removed on the target cluster's Kubernetes version can fail or behave unexpectedly.

**Not an upgrade mechanism**
- Never use backup/restore to perform a Rancher upgrade or a Kubernetes upgrade. The correct pattern for either is: take a backup → perform the upgrade normally → take a fresh backup afterward. Restoring an old backup onto an already-upgraded cluster is not supported.

**Restore scenarios**
- Restoring into a cluster that already has Rancher running is only valid if it's the **same** cluster the backup came from, and `prune: true` is set.
- Restoring into a fresh cluster (no Rancher installed) is the migration path — no special restore flags needed, but see migration rules below.
- Restore order is fixed: CRDs → cluster-scoped resources → namespaced resources.

**Migrations**
- The Rancher server's domain name must point to the new cluster after migration — it has to be the same hostname as before, just redirected.
- The target cluster must **not** already have Rancher running on it before you restore.
- Install the same Rancher version the backup came from; don't perform upgrades as part of a migration.


## 1. Create the S3 bucket

```bash
#!/bin/bash
set -eo pipefail

REGION="<YOUR_AWS_REGION>"                # e.g. ap-southeast-1, us-east-1
BUCKET_NAME="<YOUR_S3_BUCKET_NAME>"        # e.g. rancher-backups

echo "Creating S3 bucket: $BUCKET_NAME in region: $REGION..."
if [ "$REGION" = "us-east-1" ]; then
  aws s3api create-bucket \
    --bucket "$BUCKET_NAME" \
    --region "$REGION"
else
  aws s3api create-bucket \
    --bucket "$BUCKET_NAME" \
    --region "$REGION" \
    --create-bucket-configuration LocationConstraint="$REGION"
fi

echo "Enabling Block Public Access..."
aws s3api put-public-access-block \
  --bucket "$BUCKET_NAME" \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

echo "Enabling Versioning..."
aws s3api put-bucket-versioning \
  --bucket "$BUCKET_NAME" \
  --versioning-configuration Status=Enabled

echo "Applying Lifecycle Configuration..."
aws s3api put-bucket-lifecycle-configuration \
  --bucket "$BUCKET_NAME" \
  --lifecycle-configuration '{
    "Rules": [
      {
        "ID": "CleanupRancherBackups",
        "Status": "Enabled",
        "Filter": {},
        "NoncurrentVersionExpiration": { "NoncurrentDays": 30 },
        "AbortIncompleteMultipartUpload": { "DaysAfterInitiation": 7 }
      }
    ]
  }'

echo "S3 Bucket $BUCKET_NAME created and configured successfully!"
```

## 2. Create the IAM user

```bash
#!/bin/bash
set -eo pipefail

USER="<YOUR_IAM_USERNAME>"                 # e.g. rancher-s3-backup-user
BUCKET_NAME="<YOUR_S3_BUCKET_NAME>"        # e.g. company-rancher-backups

echo "Creating IAM user: $USER..."
aws iam create-user --user-name "$USER"

echo "Attaching inline Rancher backup policy for bucket: $BUCKET_NAME..."
aws iam put-user-policy --user-name "$USER" \
  --policy-name RancherBackupS3AccessPolicy \
  --policy-document '{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::'"$BUCKET_NAME"'"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject",
        "s3:PutObjectAcl"
      ],
      "Resource": [
        "arn:aws:s3:::'"$BUCKET_NAME"'/*"
      ]
    }
  ]
}'

echo "Generating access key pair..."
aws iam create-access-key --user-name "$USER"
```

> **Important:** when specifying a folder path for backups in the `Backup`/`Restore` spec, give the folder name **without** a leading or trailing `/` (e.g. `bkp`, not `/bkp/`).

## 3. Create the S3 credential secret in the Rancher cluster

> **Important:** this secret must be created in the `cattle-resources-system` namespace.

```bash
kubectl create secret generic rancher-bkp-secret \
  -n cattle-resources-system \
  --from-literal=accessKey="YOUR_AWS_ACCESS_KEY" \
  --from-literal=secretKey="YOUR_AWS_SECRET_KEY"
```

Watch the `rancher-backup` pod to confirm it's healthy:

```bash
kubectl get pods -n cattle-resources-system -l app.kubernetes.io/name=rancher-backup -w
```

**Known issue:** if the Backups page in the Rancher UI shows a `ResourceSet` error, reload the page.

## 4. Set up backup encryption

Generate a strong 32-byte base64 key:

```bash
KEY=$(head -c 32 /dev/urandom | base64)
```

> **Important:** store `$KEY` somewhere safe — it is required again when restoring from any backup encrypted with it.

Write the `EncryptionConfiguration` file:

```bash
cat <<EOF > encryption-provider-config.yaml
apiVersion: apiserver.config.k8s.io/v1
kind: EncryptionConfiguration
resources:
  - resources:
      - secrets
    providers:
      - aescbc:
          keys:
            - name: key1
              secret: ${KEY}
      - identity: {}
EOF
```


Create the encryption secret:

```bash
kubectl create secret generic rancher-backup-encryption-key \
  --from-file=./encryption-provider-config.yaml \
  -n cattle-resources-system
```

## 5. Install the rancher-backup operator

The storage location is an operator-level setting, so it's configured at install (or upgrade) time. Backups are written as `.tar.gz` files to S3/Minio or a persistent volume.

**Via the Rancher UI (recommended):**

1. Top-left ☰ → **Cluster Management**.
2. On the Clusters page, find the **local** cluster (runs the Rancher server) and click **Explore**.
3. **Apps** → **Charts**.
4. Click **Rancher Backups** → **Install**.<img width="1725" height="536" alt="image-2" src="https://github.com/user-attachments/assets/5cea10d7-4c0d-45dc-bf8d-424355577892" />

5. Configure the default storage location (S3 bucket/credentials as set up in steps 1–3).
6. Click **Install**.

**Via Terraform (`rancher2` provider):**

Installs through Rancher's own catalog system (`rancher2_app_v2`), equivalent to the UI/Helm methods above but managed as code. Chart version should match what's shown in Apps & Marketplace (confirmed here as `109.0.7+up10.0.8` for both charts):

```hcl
resource "rancher2_app_v2" "rancher_backup_crd" {
  cluster_id    = "local"
  name          = "rancher-backup-crd"
  namespace     = "cattle-resources-system"
  repo_name     = "rancher-charts"
  chart_name    = "rancher-backup-crd"
  chart_version = "<RANCHER_BACKUP_CHART_VERSION>" # e.g. "109.0.1+up10.0.3"
}

resource "rancher2_app_v2" "rancher_backup" {
  depends_on    = [rancher2_app_v2.rancher_backup_crd]
  cluster_id    = "local"
  name          = "rancher-backup"
  namespace     = "cattle-resources-system"
  repo_name     = "rancher-charts"
  chart_name    = "rancher-backup"
  chart_version = "<RANCHER_BACKUP_CHART_VERSION>" # e.g. "109.0.1+up10.0.3"

  values = <<-EOF
    s3:
      enabled: true
      bucketName: "<YOUR_S3_BUCKET_NAME>"
      folder: "<BACKUP_SUBFOLDER_NAME>"
      region: "<YOUR_AWS_REGION>"
      endpoint: "s3.<YOUR_AWS_REGION>.amazonaws.com"
      credentialSecretName: "rancher-bkp-secret"
      credentialSecretNamespace: "cattle-resources-system"
  EOF
}
```

Confirm the operator is running before creating any `Backup`/`Restore` resources:

```bash
kubectl get pods -n cattle-resources-system -l app.kubernetes.io/name=rancher-backup
```

## 6. Create a backup

To perform a backup, create a `Backup` custom resource — either via the Rancher UI (**local** cluster → **Rancher Backups** → **Backups** → **Create**) or via YAML as below.

> **Important:** `resourceSetName` must be set to either `rancher-resource-set-full` or `rancher-resource-set-basic`.
>
> The `rancher-backup` operator does **not** save the `EncryptionConfiguration` file itself — you must keep the `encryption-provider-config.yaml` file (or at minimum the key inside it) from step 4 somewhere safe, since the exact same file is required again when restoring from any backup created with it.

### One-time backup

```yaml
apiVersion: resources.cattle.io/v1
kind: Backup
metadata:
  name: <BACKUP_JOB_NAME>                             # e.g. backup-onetime-rancher
  namespace: cattle-resources-system
spec:
  resourceSetName: rancher-resource-set-full          # Options: "rancher-resource-set-full" or "rancher-resource-set-basic"
  encryptionConfigSecretName: rancher-backup-encryption-key

  # --- Target Storage Configuration ---
  storageLocation:
    s3:
      credentialSecretName: rancher-bkp-secret
      credentialSecretNamespace: cattle-resources-system
      bucketName: <YOUR_S3_BUCKET_NAME>
      folder: <BACKUP_SUBFOLDER_NAME>                 # Do NOT add leading or trailing slashes
      region: <YOUR_AWS_REGION>
      endpoint: s3.<YOUR_AWS_REGION>.amazonaws.com
```

### Recurring backup

```yaml
apiVersion: resources.cattle.io/v1
kind: Backup
metadata:
  name: <RECURRING_BACKUP_JOB_NAME>                 # e.g. recurr-bkp-rancher-daily
  namespace: cattle-resources-system
spec:
  resourceSetName: rancher-resource-set-full          # Options: "rancher-resource-set-full" or "rancher-resource-set-basic"
  encryptionConfigSecretName: rancher-backup-encryption-key

  # --- Target Storage Configuration ---
  storageLocation:
    s3:
      credentialSecretName: rancher-bkp-secret
      credentialSecretNamespace: cattle-resources-system
      bucketName: <YOUR_S3_BUCKET_NAME>
      folder: <BACKUP_SUBFOLDER_NAME>                 # Do NOT add leading or trailing slashes
      region: <YOUR_AWS_REGION>
      endpoint: s3.<YOUR_AWS_REGION>.amazonaws.com

  schedule: "@every 1h"                               # Cron format or standard descriptors (e.g. "0 2 * * *")
  retentionCount: 10
```

## 7. Monitor backups

View backup operator logs:

```bash
kubectl logs -n cattle-resources-system -l app.kubernetes.io/name=rancher-backup -f
```

List backup resources:

```bash
kubectl get backups.resources.cattle.io
```

## 7. Restore from a backup

View restore logs:

```bash
kubectl logs -n cattle-resources-system -l app.kubernetes.io/name=rancher-backup -f
```

List active restore resources:

```bash
kubectl get restores.resources.cattle.io
```

Delete an active restore resource if needed:

```bash
kubectl delete restore <restore-cr-name>
```

Restore CR:

> **Important:** `prune: true` must be set when restoring into a cluster that already has Rancher running, and only applies if it's the same cluster the backup came from. Restoring into a fresh cluster (no Rancher installed) is the migration path and doesn't require `prune`.

```yaml
apiVersion: resources.cattle.io/v1
kind: Restore
metadata:
  name: <RESTORE_JOB_NAME>                            # e.g. restore-rancher-mgmt
  namespace: cattle-resources-system
spec:
  prune: true                                         # TRUE for same-cluster rollback; FALSE for new-cluster migration
  backupFilename: <BACKUP_FILENAME>.tar.gz.enc        # Exact filename as stored in S3
  encryptionConfigSecretName: rancher-backup-encryption-key

  # --- Target Storage Configuration ---
  storageLocation:
    s3:
      credentialSecretName: rancher-bkp-secret
      credentialSecretNamespace: cattle-resources-system
      bucketName: <YOUR_S3_BUCKET_NAME>
      folder: <BACKUP_SUBFOLDER_NAME>
      region: <YOUR_AWS_REGION>
      endpoint: s3.<YOUR_AWS_REGION>.amazonaws.com
```

## Quick reference

| Task | Command |
|---|---|
| Watch backup operator pod | `kubectl get pods -n cattle-resources-system -l app.kubernetes.io/name=rancher-backup -w` |
| Tail backup/restore logs | `kubectl logs -n cattle-resources-system -l app.kubernetes.io/name=rancher-backup -f` |
| List backups | `kubectl get backups.resources.cattle.io` |
| List restores | `kubectl get restores.resources.cattle.io` |
| Cancel a restore | `kubectl delete restore <restore-cr-name>` |
