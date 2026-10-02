""" StorageClass Component: Base Class

Layer 4: Component and its implementation
"""

from abc import ABC, abstractmethod


class Base(ABC):
    """Base class for StorageClass implementations"""

    def __init__(self):
        self.unsupported_msg = f"Unsupported by {self.__class__.__name__}, falling back."

    @abstractmethod
    def create(self, name, data_engine, number_of_replicas, disk_selector):
        """Create a StorageClass"""
        raise NotImplementedError(self.unsupported_msg)

    @abstractmethod
    def delete(self, name):
        """Delete a StorageClass"""
        raise NotImplementedError(self.unsupported_msg)

    @abstractmethod
    def get(self, name):
        """Get a StorageClass by name"""
        raise NotImplementedError(self.unsupported_msg)

    @abstractmethod
    def list(self, label_selector=None):
        """List StorageClasses with optional label selector"""
        raise NotImplementedError(self.unsupported_msg)

    @abstractmethod
    def cleanup(self):
        """Cleanup test StorageClasses"""
        raise NotImplementedError(self.unsupported_msg)
