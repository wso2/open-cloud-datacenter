"""
Network Keywords - creates Network() instance and delegates
Layer 3: Keyword wrappers for Robot Framework
"""
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa E402
from utility.utility import logging  # noqa E402
from network import Network  # noqa E402


class network_keywords:
    """Network keyword wrapper - creates Network component and delegates"""

    def __init__(self):
        """Initialize network keywords with lazy loading"""
        self._network = None

    @property
    def network(self):
        """Lazy initialize network to allow API client setup first"""
        if self._network is None:
            self._network = Network()
        return self._network

    # Cluster Network Operations
    def create_cluster_network(self, name):
        """
        Create cluster network.

        Args:
            name: Cluster network name

        Returns:
            dict: Cluster network data
        """
        logging(f"Creating cluster network: {name}")
        return self.network.create_cluster_network(name)

    def delete_cluster_network(self, name):
        """
        Delete cluster network.

        Args:
            name: Cluster network name
        """
        logging(f"Deleting cluster network: {name}")
        self.network.delete_cluster_network(name)

    def create_vlan_config(self, name, cluster_network, nic):
        """
        Create VLAN config to bind NIC to cluster network.

        Args:
            name: VLAN config name
            cluster_network: Cluster network name
            nic: NIC name (e.g., eno50)

        Returns:
            dict: VLAN config data
        """
        logging(f"Creating VLAN config: {name}")
        return self.network.create_vlan_config(name, cluster_network, nic)

    def delete_vlan_config(self, name):
        """
        Delete VLAN config.

        Args:
            name: VLAN config name
        """
        logging(f"Deleting VLAN config: {name}")
        self.network.delete_vlan_config(name)

    def wait_for_cluster_network_ready(self, name, timeout=120):
        """
        Wait for cluster network to become ready.

        Args:
            name: Cluster network name
            timeout: Wait timeout in seconds

        Returns:
            dict: Cluster network data when ready
        """
        logging(f"Waiting for cluster network {name} to be ready")
        return self.network.wait_for_cluster_network_ready(
            name, int(timeout)
        )

    # VLAN Network Operations
    def create_vlan_network(self, name, vlan_id, cluster_network):
        """
        Create VLAN network.

        Args:
            name: Network name
            vlan_id: VLAN ID
            cluster_network: Cluster network name

        Returns:
            dict: Network data
        """
        logging(f"Creating VLAN network: {name}")
        return self.network.create_vlan_network(
            name, vlan_id, cluster_network
        )

    def delete_vlan_network(self, name):
        """
        Delete VLAN network.

        Args:
            name: Network name
        """
        logging(f"Deleting VLAN network: {name}")
        self.network.delete_vlan_network(name)

    # IP Pool Operations
    def get_ip_pool(self, name):
        """
        Get IP pool by name.

        Args:
            name: IP pool name

        Returns:
            dict or None: IP pool data, or None if not found
        """
        logging(f"Getting IP pool: {name}")
        return self.network.get_ip_pool(name)

    def create_ip_pool(self, name, subnet, start_ip, end_ip, network_id,
                       gateway):
        """
        Create IP pool.

        Args:
            name: IP pool name
            subnet: Subnet CIDR
            start_ip: Start IP address
            end_ip: End IP address
            network_id: Network ID
            gateway: Gateway IP address

        Returns:
            dict: IP pool data
        """
        logging(f"Creating IP pool: {name}")
        return self.network.create_ip_pool(
            name, subnet, start_ip, end_ip, network_id, gateway
        )

    def delete_ip_pool(self, name):
        """
        Delete IP pool.

        Args:
            name: IP pool name
        """
        logging(f"Deleting IP pool: {name}")
        self.network.delete_ip_pool(name)
