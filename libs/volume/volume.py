
"""
Volume Component - delegates to CRD or REST implementation
"""
import os

from constant import HarvesterOperationStrategy, DEFAULT_STORAGE_CLASS
from volume.rest import Rest
from volume.crd import CRD
from volume.base import Base


class Volume(Base):
    """Volume component - selects implementation by HARVESTER_OPERATION_STRATEGY"""
    def __init__(self):
        strategy_str = os.getenv("HARVESTER_OPERATION_STRATEGY", "crd").lower()
        if strategy_str == HarvesterOperationStrategy.REST.value:
            self.volume = Rest()
        else:
            self.volume = CRD()

    def create(self, volume_name, size, numberOfReplicas, frontend, **kwargs):
        return self.volume.create(volume_name, size, numberOfReplicas, frontend, **kwargs)

    def try_create(self, volume_name, size, numberOfReplicas=3,
                   frontend="blockdev", **kwargs):
        return self.volume.try_create(
            volume_name, size, numberOfReplicas, frontend, **kwargs)

    def exists(self, volume_name):
        return self.volume.exists(volume_name)

    def create_from_image(self, volume_name, image_name, size=None):
        return self.volume.create_from_image(volume_name, image_name, size)

    def get_image_id(self, volume_name):
        return self.volume.get_image_id(volume_name)

    def export_to_image(self, volume_name, image_name,
                        target_storage_class=DEFAULT_STORAGE_CLASS):
        return self.volume.export_to_image(
            volume_name, image_name, target_storage_class)

    def create_concurrently(self, volume_names, size="10Gi"):
        return self.volume.create_concurrently(volume_names, size)

    def delete_concurrently(self, volume_names):
        return self.volume.delete_concurrently(volume_names)

    def try_get(self, volume_name):
        return self.volume.try_get(volume_name)

    def try_delete(self, volume_name):
        return self.volume.try_delete(volume_name)

    def delete(self, volume_name, wait):
        return self.volume.delete(volume_name, wait)

    def attach(self, volume_name, node_name):
        return self.volume.attach(volume_name, node_name)

    def detach(self, volume_name):
        return self.volume.detach(volume_name)

    def wait_for_running(self, volume_name, timeout):
        return self.volume.wait_for_running(volume_name, timeout)

    def wait_for_attached(self, volume_name, timeout):
        return self.volume.wait_for_attached(volume_name, timeout)

    def wait_for_deleted(self, volume_name, timeout):
        return self.volume.wait_for_deleted(volume_name, timeout)

    def get_status(self, volume_name):
        return self.volume.get_status(volume_name)

    def list(self):
        return self.volume.list()

    def expand(self, volume_name, new_size):
        return self.volume.expand(volume_name, new_size)

    def try_expand(self, volume_name, new_size):
        return self.volume.try_expand(volume_name, new_size)

    def create_snapshot(self, volume_name, snapshot_name, snapshot_class):
        return self.volume.create_snapshot(volume_name, snapshot_name, snapshot_class)

    def delete_snapshot(self, volume_name, snapshot_name):
        return self.volume.delete_snapshot(volume_name, snapshot_name)

    def restore_from_snapshot(self, volume_name, snapshot_name, new_volume_name):
        return self.volume.restore_from_snapshot(volume_name, snapshot_name, new_volume_name)

    def wait_for_snapshot_ready(self, snapshot_name, timeout):
        return self.volume.wait_for_snapshot_ready(snapshot_name, timeout)

    def cleanup(self):
        return self.volume.cleanup()
