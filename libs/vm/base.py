"""
Base class for VM operations (optional - for common patterns)
"""
from abc import ABC, abstractmethod
from constant import DEFAULT_NAMESPACE


class Base(ABC):
    """Base class for VM implementations"""

    @abstractmethod
    def create(self, vm_name, image_id, cpu, memory, **kwargs):
        """Create a virtual machine"""
        pass

    @abstractmethod
    def delete(self, vm_name):
        """Delete a virtual machine"""
        pass

    @abstractmethod
    def start(self, vm_name):
        """Start a VM"""
        pass

    @abstractmethod
    def stop(self, vm_name):
        """Stop a VM"""
        pass

    @abstractmethod
    def restart(self, vm_name):
        """Restart a VM"""
        pass

    @abstractmethod
    def migrate(self, vm_name, target_node):
        """Migrate a VM to a target node"""
        pass

    @abstractmethod
    def wait_for_running(self, vm_name, timeout):
        """Wait for VM to be running"""
        pass

    @abstractmethod
    def wait_for_stopped(self, vm_name, timeout):
        """Wait for VM to be stopped"""
        pass

    @abstractmethod
    def wait_for_deleted(self, vm_name, timeout):
        """Wait for VM to be deleted"""
        pass

    @abstractmethod
    def list(self, namespace=DEFAULT_NAMESPACE):
        """List VMs in a namespace"""
        pass

    @abstractmethod
    def get_status(self, vm_name):
        """Get VM status"""
        pass

    @abstractmethod
    def cleanup(self):
        """Clean up test resources"""
        pass

    @abstractmethod
    def update_disk_size(self, vm_name, disk_name, new_size, namespace=DEFAULT_NAMESPACE):
        """Update VM disk size via volumeClaimTemplates annotation."""
        pass
