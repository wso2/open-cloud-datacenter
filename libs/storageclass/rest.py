""" StorageClass Component: REST Implementation

Layer 4: Component and its implementation
"""

from .base import Base


class Rest(Base):
    """REST implementation for StorageClass operations using Harvester API"""

    def __init__(self):
        super().__init__()

    def create(self, name, data_engine, number_of_replicas, disk_selector):
        return super().add_storageclass(name, data_engine, number_of_replicas, disk_selector)

    def delete(self, name):
        return super().delete(name)

    def get(self, name):
        return super().get(name)

    def list(self, label_selector=None):
        return super().list(label_selector)

    def cleanup(self):
        return super().cleanup()
