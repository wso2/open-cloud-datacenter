"""
Base class for Network operations
Defines the interface for both CRD and REST implementations
"""
from abc import ABC, abstractmethod


class Base(ABC):
    """Base class for Network implementations"""

    # Cluster Network Operations
    @abstractmethod
    def create_cluster_network(self, name):
        """Create cluster network.

        Args:
            name: Cluster network name

        Returns:
            dict: Cluster network data
        """
        pass

    @abstractmethod
    def delete_cluster_network(self, name):
        """Delete cluster network.

        Args:
            name: Cluster network name
        """
        pass

    @abstractmethod
    def cleanup_cluster_networks(self):
        pass

    @abstractmethod
    def create_vlan_config(self, name, cluster_network, nic):
        """Create VLAN config to bind NIC to cluster network.

        Args:
            name: VLAN config name
            cluster_network: Cluster network name
            nic: NIC name (e.g., eno50)

        Returns:
            dict: VLAN config data
        """
        pass

    @abstractmethod
    def delete_vlan_config(self, name):
        """Delete VLAN config.

        Args:
            name: VLAN config name
        """
        pass

    @abstractmethod
    def cleanup_vlan_configs(self):
        pass

    @abstractmethod
    def wait_for_cluster_network_ready(self, name, timeout):
        """Wait for cluster network to become ready.

        Args:
            name: Cluster network name
            timeout: Timeout in seconds

        Returns:
            dict: Cluster network data when ready
        """
        pass

    # VLAN Network Operations
    @abstractmethod
    def create_vlan_network(self, name, vlan_id, cluster_network):
        """Create VLAN network.

        Args:
            name: Network name
            vlan_id: VLAN ID
            cluster_network: Cluster network name

        Returns:
            dict: Network data
        """
        pass

    @abstractmethod
    def delete_vlan_network(self, name):
        """Delete VLAN network.

        Args:
            name: Network name
        """
        pass

    @abstractmethod
    def cleanup_vlan_networks(self):
        pass

    # IP Pool Operations
    @abstractmethod
    def get_ip_pool(self, name):
        """Get IP pool by name, returns None if not found.

        Args:
            name: IP pool name

        Returns:
            dict or None: IP pool data, or None if not found
        """
        pass

    @abstractmethod
    def create_ip_pool(self, name, subnet, start_ip, end_ip, network_id,
                       gateway):
        """Create IP pool.

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
        pass

    @abstractmethod
    def delete_ip_pool(self, name):
        """Delete IP pool.

        Args:
            name: IP pool name
        """
        pass
