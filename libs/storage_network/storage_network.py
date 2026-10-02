"""
Storage Network Component - delegates to CRD or REST implementation
Layer 4: Selects implementation based on strategy

The implementation is selected based on the HARVESTER_OPERATION_STRATEGY
environment variable. Valid values are 'crd' or 'rest'. Defaults to 'crd' if not set.
"""

import os
from constant import HarvesterOperationStrategy
from storage_network.rest import Rest
from storage_network.crd import CRD
from storage_network.base import Base


class StorageNetwork(Base):
    """
    Storage Network component that delegates to CRD or REST implementation

    The implementation is selected based on:
    - HARVESTER_OPERATION_STRATEGY environment variable ('crd' or 'rest')
    - Defaults to 'crd' if not set
    """

    def __init__(self):
        """Initialize Storage Network component"""
        strategy_str = os.getenv(
            "HARVESTER_OPERATION_STRATEGY", "crd"
        ).lower()
        try:
            self._strategy = HarvesterOperationStrategy(strategy_str)
        except ValueError:
            self._strategy = HarvesterOperationStrategy.CRD

        if self._strategy == HarvesterOperationStrategy.CRD:
            self.storage_network = CRD()
        else:
            self.storage_network = Rest()

    def enable_storage_network(self, vlan_id, cluster_network, ip_range,
                               share_rwx=False):
        """Enable the storage-network Harvester setting"""
        return self.storage_network.enable_storage_network(
            vlan_id, cluster_network, ip_range, share_rwx=share_rwx
        )

    def disable_storage_network(self):
        """Disable the storage-network Harvester setting"""
        return self.storage_network.disable_storage_network()

    def get_storage_network_status(self):
        """Get the current storage-network setting status"""
        return self.storage_network.get_storage_network_status()

    def get_vlan_network_cidr(self, vlan_id, cluster_network):
        """Get CIDR for a VLAN network"""
        return self.storage_network.get_vlan_network_cidr(
            vlan_id, cluster_network
        )

    def enable_longhorn_rwx_storage_network(self):
        """Enable the Longhorn RWX storage network setting"""
        return self.storage_network.enable_longhorn_rwx_storage_network()

    def disable_longhorn_rwx_storage_network(self):
        """Disable the Longhorn RWX storage network setting"""
        return self.storage_network.disable_longhorn_rwx_storage_network()
