"""
Base class for Volume operations (optional - for common patterns)
"""
from abc import ABC, abstractmethod


class Base(ABC):
    """Base class for Volume implementations"""

    @abstractmethod
    def create(self, volume_name, size, numberOfReplicas, frontend, **kwargs):
        """Create a volume"""
        pass

    @abstractmethod
    def delete(self, volume_name, wait):
        """Delete a volume"""
        pass

    @abstractmethod
    def get_status(self, volume_name):
        """Get volume status"""
        pass

    @abstractmethod
    def attach(self, volume_name, node_name):
        """Attach volume to a node"""
        pass

    @abstractmethod
    def detach(self, volume_name):
        """Detach volume from a node"""
        pass

    @abstractmethod
    def list(self):
        """List all volumes"""
        pass

    @abstractmethod
    def expand(self, volume_name, new_size):
        """Expand the size of a volume"""
        pass

    @abstractmethod
    def create_snapshot(self, volume_name, snapshot_name, snapshot_class):
        """Create a snapshot of the volume"""
        pass

    @abstractmethod
    def delete_snapshot(self, volume_name, snapshot_name):
        """Delete a snapshot of the volume"""
        pass

    @abstractmethod
    def restore_from_snapshot(self, volume_name, snapshot_name, new_volume_name):
        """Restore a volume from a snapshot"""
        pass

    @abstractmethod
    def wait_for_snapshot_ready(self, snapshot_name, timeout):
        """Wait for a snapshot to become ready to use"""
        pass

    @abstractmethod
    def wait_for_running(self, volume_name, timeout):
        """Wait for the volume to be in a running state"""
        pass

    @abstractmethod
    def wait_for_attached(self, volume_name, timeout):
        """Wait for the volume to be attached"""
        pass

    @abstractmethod
    def wait_for_deleted(self, volume_name, timeout):
        """Wait for the volume to be deleted"""
        pass

    @abstractmethod
    def cleanup(self):
        """Clean up all test volumes"""
        pass
