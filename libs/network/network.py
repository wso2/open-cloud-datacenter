"""
Network Component - delegates to CRD or REST implementation
Layer 4: Selects implementation based on strategy

The implementation is selected based on the HARVESTER_OPERATION_STRATEGY
environment variable. Valid values are 'crd' or 'rest'. Defaults to 'crd' if not set.
"""

import os
from constant import HarvesterOperationStrategy
from network.rest import Rest
from network.crd import CRD
from network.base import Base


class Network(Base):
    """
    Network component that delegates to CRD or REST implementation

    The implementation is selected based on:
    - HARVESTER_OPERATION_STRATEGY environment variable ('crd' or 'rest')
    - Defaults to 'crd' if not set
    """

    def __init__(self):
        """Initialize Network component"""
        strategy_str = os.getenv(
            "HARVESTER_OPERATION_STRATEGY", "crd"
        ).lower()
        try:
            self._strategy = HarvesterOperationStrategy(strategy_str)
        except ValueError:
            self._strategy = HarvesterOperationStrategy.CRD

        if self._strategy == HarvesterOperationStrategy.CRD:
            self.network = CRD()
        else:
            self.network = Rest()

    # Cluster Network Operations
    def create_cluster_network(self, name):
        """Create cluster network"""
        return self.network.create_cluster_network(name)

    def delete_cluster_network(self, name):
        """Delete cluster network"""
        return self.network.delete_cluster_network(name)

    def cleanup_cluster_networks(self):
        return self.network.cleanup_cluster_networks()

    def create_vlan_config(self, name, cluster_network, nic):
        """Create VLAN config to bind NIC to cluster network"""
        return self.network.create_vlan_config(
            name, cluster_network, nic
        )

    def delete_vlan_config(self, name):
        """Delete VLAN config"""
        return self.network.delete_vlan_config(name)

    def cleanup_vlan_configs(self):
        return self.network.cleanup_vlan_configs()

    def wait_for_cluster_network_ready(self, name, timeout=120):
        """Wait for cluster network to become ready"""
        return self.network.wait_for_cluster_network_ready(name, timeout)

    # VLAN Network Operations
    def create_vlan_network(self, name, vlan_id, cluster_network):
        """Create VLAN network"""
        return self.network.create_vlan_network(
            name, vlan_id, cluster_network
        )

    def delete_vlan_network(self, name):
        """Delete VLAN network"""
        return self.network.delete_vlan_network(name)

    def cleanup_vlan_networks(self):
        return self.network.cleanup_vlan_networks()

    # IP Pool Operations
    def get_ip_pool(self, name):
        """Get IP pool by name"""
        return self.network.get_ip_pool(name)

    def create_ip_pool(self, name, subnet, start_ip, end_ip, network_id,
                       gateway):
        """Create IP pool"""
        return self.network.create_ip_pool(
            name, subnet, start_ip, end_ip, network_id, gateway
        )

    def delete_ip_pool(self, name):
        """Delete IP pool"""
        return self.network.delete_ip_pool(name)
