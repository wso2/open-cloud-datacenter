"""
Constants for Harvester test framework
"""

import os
from enum import Enum


# Operation Strategy
class HarvesterOperationStrategy(Enum):
    """Strategy for Harvester operations - REST API or CRD"""
    REST = "rest"
    CRD = "crd"


# Harvester API Groups
HARVESTER_API_GROUP = "harvesterhci.io"
HARVESTER_API_VERSION = "v1beta1"
KUBEVIRT_API_GROUP = "kubevirt.io"
KUBEVIRT_API_VERSION = "v1"
LONGHORN_API_GROUP = "longhorn.io"
LONGHORN_API_VERSION = "v1beta2"

# Harvester Namespace
HARVESTER_NAMESPACE = "harvester-system"
HARVESTER_PUBLIC_NAMESPACE = "harvester-public"
DEFAULT_NAMESPACE = "default"
LONGHORN_NAMESPACE = "longhorn-system"

# Harvester Resource Plurals
VIRTUALMACHINE_PLURAL = "virtualmachines"
VIRTUALMACHINEIMAGE_PLURAL = "virtualmachineimages"
VIRTUALMACHINEINSTANCE_PLURAL = "virtualmachineinstances"
VIRTUALMACHINEBACKUP_PLURAL = "virtualmachinebackups"
VIRTUALMACHINERESTORE_PLURAL = "virtualmachinerestores"
VIRTUALMACHINETEMPLATE_PLURAL = "virtualmachinetemplates"
VIRTUALMACHINETEMPLATEVERSION_PLURAL = "virtualmachinetemplateversions"
VOLUME_PLURAL = "volumes"
PERSISTENTVOLUMECLAIM_PLURAL = "persistentvolumeclaims"
VIRTUALMACHINEBACKUP_PLURAL = "virtualmachinebackups"

# Backup / Restore
SETTING_BACKUP_TARGET = "backup-target"
BACKUP_TYPE_BACKUP = "backup"
DELETION_POLICY_DELETE = "delete"
DELETION_POLICY_RETAIN = "retain"

# Longhorn backup-store bookkeeping CRs (longhorn-system namespace)
BACKUPVOLUME_PLURAL = "backupvolumes"
BACKUPBACKINGIMAGE_PLURAL = "backupbackingimages"
# Prefix of UID-style longhorn BackingImage names created by Harvester
BACKING_IMAGE_PREFIX = "vmi"

# Test Labels
LABEL_TEST = "harvesterhci.io/test"
LABEL_TEST_VALUE = "robot-framework"

# Size Constants
KIBIBYTE = 1024
MEBIBYTE = (KIBIBYTE * KIBIBYTE)
GIBIBYTE = (MEBIBYTE * KIBIBYTE)
TEBIBYTE = (GIBIBYTE * KIBIBYTE)
LARGE_DISK_BYTE = TEBIBYTE

# VM States
VM_STATE_STOPPED = "Stopped"
VM_STATE_RUNNING = "Running"
VM_STATE_STARTING = "Starting"
VM_STATE_STOPPING = "Stopping"
VM_STATE_MIGRATING = "Migrating"

# Volume States
VOLUME_STATE_BOUND = "Bound"
VOLUME_STATE_PENDING = "Pending"

# Image States
IMAGE_STATE_ACTIVE = "Active"
IMAGE_STATE_IMPORTING = "Importing"
IMAGE_STATE_FAILED = "Failed"

# VM Frontend Types
VOLUME_FRONTEND_BLOCKDEV = "blockdev"
VOLUME_FRONTEND_ISCSI = "iscsi"

# NVIDIA Driver Toolkit Addon Defaults
NVIDIA_TOOLKIT_IMAGE_REPO = "nvidia/driver-toolkit"
NVIDIA_TOOLKIT_IMAGE_TAG = "latest"
NVIDIA_TOOLKIT_DRIVER_LOCATION = "/drivers"

# Access Modes
ACCESS_MODE_RWO = "ReadWriteOnce"
ACCESS_MODE_RWX = "ReadWriteMany"
ACCESS_MODE_ROX = "ReadOnlyMany"

# Image Source Types
IMAGE_SOURCE_DOWNLOAD = "download"
IMAGE_SOURCE_UPLOAD = "upload"
IMAGE_SOURCE_EXPORT = "export-from-volume"

# Network Types
NETWORK_TYPE_VLAN = "vlan"
NETWORK_TYPE_UNTAGGED = "untagged"

# Annotations
ANNOT_REPLICA_NAMES = "harvesterhci.io/replica-names"
ANNOT_DATA_CHECKSUM = "test.harvesterhci.io/data-checksum-"
ANNOT_LAST_CHECKSUM = "test.harvesterhci.io/last-recorded-checksum"
ANNOT_DESCRIPTION = "field.cattle.io/description"

# VM Run Strategies
RUN_STRATEGY_ALWAYS = "Always"
RUN_STRATEGY_HALTED = "Halted"
RUN_STRATEGY_MANUAL = "Manual"
RUN_STRATEGY_RERUN_ON_FAILURE = "RerunOnFailure"

# Storage Class
DEFAULT_STORAGE_CLASS = "harvester-longhorn"

# VolumeSnapshotClass used by Longhorn-backed snapshots on Harvester
DEFAULT_VOLUME_SNAPSHOT_CLASS = "longhorn-snapshot"

# Retry and Timeout Defaults
DEFAULT_RETRY_COUNT = 100
DEFAULT_RETRY_INTERVAL = 3

# Timeout constants (in seconds)
DEFAULT_TIMEOUT_SHORT = 3600
DEFAULT_TIMEOUT = 3600
DEFAULT_TIMEOUT_LONG = 3600

# Addon Names
ADDON_RANCHER_MONITORING = "rancher-monitoring"
ADDON_RANCHER_LOGGING = "rancher-logging"
ADDON_VM_IMPORT_CONTROLLER = "vm-import-controller"

# Monitoring Namespace
MONITORING_NAMESPACE = "cattle-monitoring-system"

# Addon-related resource plurals
ADDON_PLURAL = "addons"

# Descheduler Addon Constants
# The addon CR ships disabled by default in kube-system; enabling it renders a
# Deployment and a ConfigMap both named "descheduler" from the upstream
# kubernetes-sigs/descheduler chart (release name == chart name).
ADDON_DESCHEDULER = "descheduler"
DESCHEDULER_NAMESPACE = "kube-system"
DESCHEDULER_LABEL = "app.kubernetes.io/name=descheduler"
DESCHEDULER_DEPLOYMENT = "descheduler"
DESCHEDULER_CONFIGMAP = "descheduler"
DESCHEDULER_POLICY_KEY = "policy.yaml"
# Harvester marks non-migratable VMs with this annotation so the descheduler
# never evicts them (pkg/controller/master/virtualmachine/vmi_descheduler_controller.go)
ANNOT_PREFER_NO_EVICTION = "descheduler.alpha.kubernetes.io/prefer-no-eviction"
# Bypasses the "not enough nodes" webhook check (pkg/webhook/resources/addon/validator.go)
ANNOT_SKIP_DESCHEDULER_WEBHOOK_CHECK = "harvesterhci.io/skipDeschedulerAddonWebhookCheck"

# PCI Devices Controller Addon Constants
ADDON_PCIDEVICES = "pcidevices-controller"
PCIDEVICES_NAMESPACE = "harvester-system"
PCIDEVICES_CONTROLLER_LABEL = "app.kubernetes.io/name=harvester-pcidevices-controller"
PCIDEVICES_WEBHOOK_SERVICE = "pcidevices-webhook"

# Rancher Integration Constants
RANCHER_WAIT_TIMEOUT = 3600  # 30 minutes for cluster operations
RANCHER_NAMESPACE = "fleet-default"

# Cloud-init user data for RKE2 nodes
DEFAULT_RKE2_USER_DATA = """#cloud-config
password: password
chpasswd:
  expire: false
ssh_pwauth: true
package_update: true
packages:
  - qemu-guest-agent
runcmd:
  - - systemctl
    - enable
    - '--now'
    - qemu-guest-agent.service
"""

# RKE2 Kubernetes versions (defaults)
DEFAULT_RKE2_VERSION = "v1.33"

# Tenancy (Rancher Projects / Namespaces on the Harvester local cluster)
LOCAL_CLUSTER_ID = "local"
EXISTING_HARVESTER_NAME = os.getenv(
  "EXISTING_HARVESTER_NAME", LOCAL_CLUSTER_ID
)
PROJECT_PLURAL = "projects"
PROJECT_ID_PREFIX = "p-"
# Namespace <-> project binding and per-namespace quota (Rancher conventions)
ANNOT_PROJECT_ID = "field.cattle.io/projectId"
LABEL_PROJECT_ID = "field.cattle.io/projectId"
ANNOT_RESOURCE_QUOTA = "field.cattle.io/resourceQuota"
# Rancher quota keys used for CPU / memory limits
QUOTA_LIMITS_CPU = "limitsCpu"
QUOTA_LIMITS_MEMORY = "limitsMemory"
# Namespace phases
NAMESPACE_PHASE_ACTIVE = "Active"
NAMESPACE_PHASE_TERMINATING = "Terminating"


# Rancher API groups and versions
RANCHER_MGMT_GROUP = "management.cattle.io"
RANCHER_MGMT_VERSION = "v3"
RANCHER_PROVISIONING_GROUP = "provisioning.cattle.io"
RANCHER_PROVISIONING_VERSION = "v1"

# Harvester cloud provider deployments
HARVESTER_CLOUD_PROVIDER_DEPLOYMENT = "harvester-cloud-provider"
HARVESTER_CSI_DRIVER_DEPLOYMENT = "harvester-csi-driver-controllers"

# Default VM config for RKE2 nodes
DEFAULT_RKE2_NODE_CPUS = 4
DEFAULT_RKE2_NODE_MEMORY = 8  # GB
DEFAULT_RKE2_NODE_DISK = 80  # GB
