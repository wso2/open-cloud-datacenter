
"""
Layer 3: Volume Keywords - creates Volume() instance and delegates - NO direct API calls!
"""
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging # noqa E402
from volume import Volume # noqa E402
from constant import DEFAULT_TIMEOUT_SHORT # noqa E402
from constant import DEFAULT_VOLUME_SNAPSHOT_CLASS # noqa E402
from constant import DEFAULT_STORAGE_CLASS # noqa E402


class volume_keywords:
    """Layer 3: Volume keyword wrapper - creates Volume component and delegates"""

    def __init__(self):
        self.volume = Volume()

    def cleanup_volumes(self):
        """Clean up all test volumes"""
        self.volume.cleanup()

    def create_volume(self, volume_name, size="2Gi", numberOfReplicas=3, frontend="blockdev", **kwargs):    # NOQA
        """Create a volume"""
        logging(f'Creating volume {volume_name}')
        self.volume.create(volume_name, size, numberOfReplicas, frontend, **kwargs)

    def try_create_volume(self, volume_name, size, numberOfReplicas=3):
        """Attempt to create a volume for negative testing.

        Returns a result dict {success, code, message} instead of raising, so
        the test layer can assert on the rejection.
        """
        logging(f'Attempting to create volume {volume_name} with size {size} '
                f'(negative test)')
        return self.volume.try_create(volume_name, size, numberOfReplicas)

    def volume_exists(self, volume_name):
        """Return True if the volume exists, False otherwise"""
        return self.volume.exists(volume_name)

    def create_volume_from_image(self, volume_name, image_name, size=None):
        """Create a volume backed by a VirtualMachineImage"""
        logging(f'Creating volume {volume_name} from image {image_name}')
        return self.volume.create_from_image(volume_name, image_name, size)

    def get_volume_image_id(self, volume_name):
        """Return the imageId annotation of a volume"""
        return self.volume.get_image_id(volume_name)

    def export_volume_to_image(self, volume_name, image_name,
                               target_storage_class=DEFAULT_STORAGE_CLASS):
        """Start exporting a volume to a VM image"""
        logging(f'Exporting volume {volume_name} to image {image_name}')
        return self.volume.export_to_image(
            volume_name, image_name, target_storage_class)

    def create_volumes_concurrently(self, volume_names, size="10Gi"):
        """Create multiple volumes in parallel; returns per-volume result dicts"""
        logging(f'Creating {len(volume_names)} volumes concurrently')
        return self.volume.create_concurrently(volume_names, size)

    def delete_volumes_concurrently(self, volume_names):
        """Delete multiple volumes in parallel; returns per-volume result dicts"""
        logging(f'Deleting {len(volume_names)} volumes concurrently')
        return self.volume.delete_concurrently(volume_names)

    def try_get_volume(self, volume_name):
        """Attempt to get a volume for negative testing; returns result dict"""
        logging(f'Attempting to get volume {volume_name} (negative test)')
        return self.volume.try_get(volume_name)

    def try_delete_volume(self, volume_name):
        """Attempt to delete a volume for negative testing; returns result dict"""
        logging(f'Attempting to delete volume {volume_name} (negative test)')
        return self.volume.try_delete(volume_name)

    def delete_volume(self, volume_name, wait=True):
        """Delete a volume"""
        logging(f'Deleting volume {volume_name}')
        self.volume.delete(volume_name, wait)

    def attach_volume(self, volume_name, node_name):
        """Attach volume to node"""
        logging(f'Attaching volume {volume_name} to node {node_name}')
        self.volume.attach(volume_name, node_name)

    def detach_volume(self, volume_name):
        """Detach volume"""
        logging(f'Detaching volume {volume_name}')
        self.volume.detach(volume_name)

    def wait_for_volume_running(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for volume to be running"""
        logging(f'Waiting for volume {volume_name} to be running')
        self.volume.wait_for_running(volume_name, timeout)

    def wait_for_volume_attached(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for volume to be attached"""
        logging(f'Waiting for volume {volume_name} to be attached')
        self.volume.wait_for_attached(volume_name, timeout)

    def wait_for_volume_deleted(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for volume to be deleted"""
        logging(f'Waiting for volume {volume_name} to be deleted')
        self.volume.wait_for_deleted(volume_name, timeout)

    def get_volume_status(self, volume_name):
        """Get volume status"""
        return self.volume.get_status(volume_name)

    def list_volumes(self):
        """List all volumes"""
        logging('Listing all volumes')
        return self.volume.list()

    def expand_volume(self, volume_name, new_size):
        """Expand volume size"""
        self.volume.expand(volume_name, new_size)

    def try_expand_volume(self, volume_name, new_size):
        """Attempt to resize a volume for negative testing; returns result dict"""
        logging(f'Attempting to resize volume {volume_name} to {new_size} '
                f'(negative test)')
        return self.volume.try_expand(volume_name, new_size)

    def create_volume_snapshot(self, volume_name, snapshot_name,
                               snapshot_class=DEFAULT_VOLUME_SNAPSHOT_CLASS):
        """Create volume snapshot"""
        logging(f'Creating snapshot {snapshot_name} for volume {volume_name} '
                f'using snapshot class {snapshot_class}')
        self.volume.create_snapshot(volume_name, snapshot_name, snapshot_class)

    def delete_volume_snapshot(self, volume_name, snapshot_name):
        """Delete volume snapshot"""
        logging(f'Deleting snapshot {snapshot_name} from volume {volume_name}')
        self.volume.delete_snapshot(volume_name, snapshot_name)

    def restore_volume_from_snapshot(self, volume_name, snapshot_name, new_volume_name):
        """Restore volume from snapshot"""
        logging(f'Restoring volume from snapshot {snapshot_name}')
        self.volume.restore_from_snapshot(volume_name, snapshot_name, new_volume_name)

    def wait_for_snapshot_ready(self, snapshot_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for a volume snapshot to become ready to use"""
        logging(f'Waiting for snapshot {snapshot_name} to be ready')
        self.volume.wait_for_snapshot_ready(snapshot_name, timeout)
