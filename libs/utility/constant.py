# Kubernetes constants
LONGHORN_NAMESPACE = "longhorn-system"
HARVESTER_NAMESPACE = "harvester-system"
DEFAULT_NAMESPACE = "default"

# Annotation keys
ANNOT_CHECKSUM = "harvesterhci.io/checksum"
ANNOT_IMAGE_ID = "harvesterhci.io/imageId"
ANNOT_NETWORK_IPS = "harvesterhci.io/ips"

# Label keys
LABEL_TEST = "harvesterhci.io/test"
LABEL_TEST_VALUE = "robot-framework"
LABEL_VM_NAME = "harvesterhci.io/vmName"

# VM states
VM_STATE_RUNNING = "Running"
VM_STATE_STOPPED = "Stopped"
VM_STATE_STARTING = "Starting"
VM_STATE_STOPPING = "Stopping"
VM_STATE_MIGRATING = "Migrating"

# Image states
IMAGE_STATE_ACTIVE = "Active"
IMAGE_STATE_FAILED = "Failed"

# Volume states
VOLUME_STATE_BOUND = "Bound"
VOLUME_STATE_PENDING = "Pending"

# Backup states
BACKUP_STATE_READY = "Ready"
BACKUP_STATE_ERROR = "Error"
BACKUP_STATE_IN_PROGRESS = "InProgress"

# Timeouts (seconds)
DEFAULT_TIMEOUT = 600
BACKUP_TIMEOUT = 1800
UPGRADE_TIMEOUT = 7200

# Retry intervals (seconds)
DEFAULT_RETRY_INTERVAL = 3
BACKUP_RETRY_INTERVAL = 10
