# Harvester Upgrade Plan

## Preflight Checks

### 1. Virtual Machine Management Through the Upgrade

#### Live-Migratable Virtual Machines

Live-migratable virtual machines are automatically migrated to other nodes via batch migration before the current node is upgraded. These virtual machines experience zero downtime during migration.

#### Non-Migratable Virtual Machines

When an upgrade is triggered, Harvester performs certain actions depending on the value of the upgrade-config setting's `restoreVM` option:

- `false`: Harvester does not perform the upgrade when non-migratable virtual machines are still running. You must manually power off the virtual machines.
- `true`: Harvester automatically powers off non-migratable virtual machines when the node is upgraded and then restores them after the node is rebooted.

> ⚠️ **CAUTION:** Non-migratable virtual machines experience downtime during the node upgrade and reboot.

**How to View and Modify the `restoreVM` Setting**

View current setting (CLI):

```bash
kubectl get settings.harvesterhci.io upgrade-config -o yaml
```

### 2. Back Up Your VMs

Back up all critical virtual machines before starting the upgrade process to ensure data recovery in case of unexpected host or storage failures during the upgrade.

### 3. Cluster Administrative Freeze

Do not operate the cluster during an upgrade (e.g., creating new VMs, uploading new images, altering network configurations, etc.) to prevent resource race conditions and cluster state mismatch.

### 4. Hardware Requirements

Make sure your hardware meets the [preferred requirements](https://docs.harvesterhci.io/v1.8/install/requirements/#hardware-requirements) due to intermediate resources consumed by the upgrade process (e.g., temporary upgrade containers, additional memory overhead, and volume replication transfers).

### 5. Free Partition Space

Make sure each node has at least 30 GiB of free system partition space (`/usr/local/`). If any node in the cluster has less than 30 GiB of free space, the upgrade will be denied.

Bash verification command:

```bash
# Run on each host to verify free system partition space:
df -h /usr/local/
```

### 6. Run Pre-check Script

Run the official [pre-check script](https://github.com/harvester/upgrade-helpers/tree/main/pre-check) on a Harvester control-plane node. Take action on any failed checks before initiating the upgrade process.

### 7. Pod Security Admission (PSA)

A number of one-off privileged pods will be created in the `harvester-system` and `cattle-system` namespaces to perform host-level upgrade operations. If [pod security admission](https://kubernetes.io/docs/concepts/security/pod-security-admission/) is enabled, adjust these policies to allow these pods to run without restrictions.

### 8. Time Synchronization (NTP)

> ⚠️ **CAUTION:** Make sure all nodes' times are in sync. Using an NTP server to synchronize time is recommended. If an NTP server is not configured during installation, manually configure it on each node:

```bash
sudo -i

# Add time servers
vim /etc/systemd/timesyncd.conf
# [Time]
# NTP=0.pool.ntp.org

# Enable and start systemd-timesyncd
timedatectl set-ntp true

# Check status
sudo timedatectl status
```

### 9. Network Interfaces (NICs)

> ⚠️ **CAUTION:** NICs that connect to a PCI bridge might be renamed after an upgrade. Please review relevant knowledge base articles for your hardware vendor prior to initiating the node reboot sequence.

## Start an Upgrade

> **Note:** If the Upgrade button is not visible in the Harvester dashboard UI, treat the upgrade as an [air-gapped upgrade](https://docs.harvesterhci.io/v1.8/upgrade/index/#prepare-an-air-gapped-upgrade).

Follow the steps described below to proceed with the upgrade. There are two methods, depending on whether cluster hosts have outbound internet access.

### Method 1: Internet-Connected Upgrade

If the hosts can reach the internet and port 443 is open, you can apply the upstream version manifest directly — Harvester will download the ISO from `releases.rancher.com` itself. No local ISO hosting is required.

```bash
sudo -i
kubectl create -f https://releases.rancher.com/harvester/v1.8.2/version.yaml
```

Once the custom resource is created, the Upgrade button becomes visible on the Harvester dashboard. Skip ahead to [Trigger the Upgrade](#trigger-the-upgrade) below.

### Method 2: Air-Gapped Upgrade (No Internet Access)

If hosts do not have internet access, host the ISO and a modified version manifest on a local HTTP server (e.g. a simple Python HTTP server) reachable by all cluster nodes.

#### Prerequisites & File Preparation

1. **Prepare the ISO File:**
   - Download the desired Harvester ISO file from the official GitHub [Releases](https://github.com/harvester/harvester/releases) page.
   - Save the ISO file to an internal HTTP server accessible by all cluster nodes (e.g. `python3 -m http.server 80`).
   - Example assumed URL: `http://10.10.0.1/harvester.iso`

2. **Prepare the Version Manifest:**
   - Download the corresponding version file: `https://releases.rancher.com/harvester/{version}/version.yaml`
   - Modify the `isoURL` field to point to your local HTTP server:

```yaml
apiVersion: harvesterhci.io/v1beta1
kind: Version
metadata:
  name: v1.8.2
  namespace: harvester-system
spec:
  isoChecksum: <SHA-512 checksum of the ISO>
  isoURL: http://10.10.0.1/harvester.iso  # Local ISO URL
  releaseDate: '20250425'
```

   Host this updated file on your local server as well (e.g., `http://10.10.0.1/version.yaml`).

## Trigger the Upgrade

### 1. UI Method

1. Navigate to **Harvester UI > Advanced > Settings**.
2. Locate the `server-version` row, click the ⋮ (three-dot actions menu) button on the right, and select **Upgrade**.
3. Select one of the following methods to start the upgrade:
   - **Upload New Image:**
     - Enter a **Name** for the image.
     - Select a **Source Type**:
       - **Upload:** Click **Upload File** and select a local `.iso` file from your workstation.
       - **Download:** Enter the URL of the hosted ISO file (e.g., `http://10.10.0.1/harvester.iso`) for Harvester to download directly.
     - (Optional) Select the **Enable Logging** checkbox if you want to capture upgrade logs.
     - Click **Upgrade** once the file upload or download completes.
   - **Select Existing Image:**
     - Select a previously imported Harvester upgrade image from the dropdown list.
     - Click **Upgrade**.

### 2. CLI Method

1. Access one of the control plane nodes via SSH and switch to the root account:

```bash
rancher@node1:~> sudo -i
```

2. Create the version custom resource directly using kubectl:

```bash
rancher@node1:~> kubectl create -f http://10.10.0.1/version.yaml
```

Once the custom resource is created, the Upgrade button will become visible on the Harvester dashboard. Click the button to display the upgrade interface.

## Stopping an Upgrade

Below outlines the phases in which we can stop and restart the upgrade process.

### Phase 1 & 2: Provisioning Upgrade Repo & Container Image Preloading

- **Action:** Safe to Stop and Restart
- **Guidance:** Failures at these early stages are typically caused by network speed or transient resource issues. It is recommended to first inspect the status of the repository VM/pod (`harvester-system`) or job logs (`cattle-system`). Once the underlying issue is resolved, you can safely stop and restart the upgrade.

### Phase 3: Upgrade System Services

- **Action:** Stop to Collect Diagnostics First
- **Guidance:** If a failure occurs while upgrading component Helm charts, you must generate a support bundle before taking any further action or attempting a restart. This ensures you preserve critical logs and resource manifests needed to identify the root cause of the failure.

### Phase 4: Upgrade Nodes

- **Action:** ⚠️ DO NOT Stop or Restart directly
- **Guidance:** Because this stage involves live-migrating VMs, updating the RKE2 runtime, or rebooting the operating system, stopping or restarting the upgrade is strictly not recommended without explicit guidance.

### Stop the Ongoing Upgrade

1. Log in to a control plane node.

2. List the Upgrade CRs in the cluster:

```bash
# become root
$ sudo -i

# list the on-going upgrade
$ kubectl get upgrade.harvesterhci.io -n harvester-system -l harvesterhci.io/latestUpgrade=true

NAME AGE
hvst-upgrade-9gmg2 10m
```

3. Delete the Upgrade CR:

```bash
$ kubectl delete upgrade.harvesterhci.io/hvst-upgrade-9gmg2 -n harvester-system
```


## Troubleshooting

Below section contains a description of troubleshooting each of the stages in the upgrade.

### 1. Download Upgrade Image

The `importer-prime-` pod in the `harvester-system` namespace downloads the image from the HTTP server and converts the image into a raw disk image. The process involves two phases:

1. **First Phase - Transfer/Scratch** (Downloading image from HTTP server)
2. **Second Phase - Convert** (Converting to raw disk image)

You can check the kubectl logs of the pod to view any issues in the importing of the image.

### 2. Creating Upgrade Repository

You can track the creation of the repository by checking the logs of the `upgrade-repo` pod:

```bash
kubectl logs -f upgrade-repo-hvst-upgrade-4xrvs-779c476ddb-t92tj -n harvester-system
```

This pod mounts the ISO and starts Nginx, creating the local repository for image preloading to the nodes. Look for log entries indicating successful mounting, such as:

```
iso mounted successfully to /srv/www/htdocs/harvester-iso.
```

### 3. Upgrading System Service

The `hvst-upgrade-4xrvs-apply-manifests-dsb9l` pod is responsible for upgrading the cluster control plane, management layer, and system charts before the physical host nodes are rebooted one by one.

### 4. Upgrading Node

There are multiple sub-stages in this stage.

#### Image Preloading

You can verify the image preloading progress on a node by checking the logs of the `apply-hvst-upgrade` pod:

```bash
kubectl logs -f apply-hvst-upgrade-4xrvs-prepare-on-node1-with-1e41111309-jmv4f -n cattle-system
```

This pod performs image preloading from the local repository created in the previous stage, starting from node1, then moving to node2, and so on.

#### Pre-draining

By the time pre-draining starts, the node is already at the "Images preloaded" state.

- If this is the **first** node being upgraded, pre-draining simply migrates all VMs off the node.
- If this node is upgraded **after** another node has already gone through the process, pre-draining waits until all Longhorn volumes finish rebuilding (replica rebuild on the already-upgraded node) before it starts migrating VMs off this node.


To see the remaining VM migrations in this stage:

```bash
kubectl get vmim -A
```

To view node upgrade jobs:

```bash
kubectl get jobs -n harvester-system -l harvesterhci.io/upgradeComponent=node
```

To view job logs:

```bash
kubectl logs -n harvester-system jobs/hvst-upgrade-4xrvs-pre-drain-node2
```

#### Post-drain

In the post-drain stage, the node's OS image is upgraded and the node is rebooted.

Monitor upgrade jobs for post-drain status.

To view job logs:

```bash
kubectl logs -n harvester-system jobs/hvst-upgrade-4xrvs-post-drain-node2
```

## Issues

### 1. RWX Volume of importer-prime Pod Not Mounting

> **Note:** From Harvester 1.8.0, the `storage-network-for-rwx-volume-enabled` setting was renamed to `endpoint-network-for-rwx-volume`.

The `importer-prime-` pod's RWX volume fails to mount. This is caused by the storage network settings for RWX volumes not being applied properly.

Check the settings:

```bash
# Harvester/Longhorn < 1.8.0:
kubectl get settings.longhorn.io storage-network storage-network-for-rwx-volume-enabled -n longhorn-system

# Harvester/Longhorn >= 1.8.0:
kubectl get settings.longhorn.io storage-network endpoint-network-for-rwx-volume -n longhorn-system
```

Check whether the `longhorn-csi-plugin` pods have an interface on the storage network:

```bash
kubectl get pod -n longhorn-system -l app=longhorn-csi-plugin -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}{"\n\n"}{end}'
```

Both settings show `applied=false`, and the `csi-plugin` pods do not have an interface from the storage network.

Applying the RWX setting requires downtime: all volumes need to be detached, and the `longhorn-manager` deployment needs to be rollout restarted for the change to take effect.

### 2. Pre-check Script Fails Due to Managed Chart Drift

The pre-check script fails because of a replica count mismatch on the Longhorn storage class, caused by drift in the `harvester` managed chart.

Patch the managed chart to correct the Longhorn default replica count:

```bash
kubectl -n fleet-local patch managedcharts.management.cattle.io harvester --type=merge -p '{"spec":{"values":{"longhorn":{"persistence":{"defaultClassReplicaCount":2}}}}}'
```

Restart the fleet agent to pick up the change:

```bash
kubectl -n cattle-fleet-local-system delete pod -l app=fleet-agent
```

Re-run the pre-check script to confirm the drift is resolved.

### 3. Pre-check Fails Due to Single-Replica Volumes

The pre-check script also fails when it finds single-replica volumes in a running state. The recommended action is to increase the replica count of those volumes to 2.

```bash
./check.sh | awk '/single-replica volume in running state/ {print $2}' | xargs -I {} kubectl patch lhv {} -n longhorn-system --type=merge -p '{"spec":{"numberOfReplicas":2}}'
```

### 4. VM Fails to Migrate in the Pre-drain Stage

If a VM fails to migrate during the pre-drain stage (e.g. a large or busy VM whose migration cannot converge), only then throttle its vCPU and memory by enabling auto-converge via a `MigrationPolicy`:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: migrations.kubevirt.io/v1alpha1
kind: MigrationPolicy
metadata:
  name: prod-vm-autoconverge
spec:
  allowAutoConverge: true
  selectors:
    virtualMachineInstanceSelector:
      harvesterhci.io/vmName: prod-vm
EOF
```

> ⚠️ **CAUTION:** `allowAutoConverge` lets KubeVirt/QEMU throttle the VM's vCPU to slow down its dirty memory page rate so the migration can catch up and complete. While this is in effect, the VM — and the application running on it — will experience degraded CPU performance and may become noticeably slower or less responsive until the migration finishes.

## Further Information

- https://docs.harvesterhci.io/v1.8/upgrade/index