
"""
Layer 3: Host Keywords - creates Host() instance and delegates - NO direct API calls!
"""
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging # noqa E402
from host import Host # noqa E402
from constant import DEFAULT_TIMEOUT_SHORT, DEFAULT_TIMEOUT # noqa E402


class host_keywords:
    """Layer 3: Host keyword wrapper - creates Host component and delegates"""

    def __init__(self):
        self.host = Host()

    def cleanup_hosts(self):
        """Clean up all test hosts"""
        self.host.cleanup()

    def list_nodes(self):
        """List all nodes"""
        logging('Listing all nodes')
        return self.host.list_nodes()

    def get_node_count(self):
        """Get total number of nodes in the cluster"""
        logging('Getting node count')
        return self.host.get_node_count()

    def get_node(self, node_name):
        """Get node details"""
        logging(f'Getting node {node_name}')
        return self.host.get_node(node_name)

    def get_node_by_index(self, index):
        """Get node by index"""
        logging(f'Getting node at index {index}')
        return self.host.get_node_by_index(int(index))

    def get_worker_nodes(self):
        """Get all worker nodes"""
        logging('Getting worker nodes')
        return self.host.get_worker_nodes()

    def get_control_plane_nodes(self):
        """Get all control-plane nodes"""
        logging('Getting control-plane nodes')
        return self.host.get_control_plane_nodes()

    def get_witness_nodes(self):
        return self.host.get_witness_nodes()

    def get_standard_nodes(self):
        return self.host.get_standard_nodes()

    def get_node_status(self, node_name):
        """Get node status"""
        logging(f'Getting status for node {node_name}')
        return self.host.get_node_status(node_name)

    def is_node_ready(self, node_name):
        """Check if node is ready"""
        return self.host.is_node_ready(node_name)

    def wait_for_node_ready(self, node_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for node to be ready"""
        logging(f'Waiting for node {node_name} to be ready')
        self.host.wait_for_node_ready(node_name, timeout)

    def cordon_node(self, node_name):
        """Cordon a node"""
        logging(f'Cordoning node {node_name}')
        self.host.cordon_node(node_name)

    def uncordon_node(self, node_name):
        """Uncordon a node"""
        logging(f'Uncordoning node {node_name}')
        self.host.uncordon_node(node_name)

    def drain_node(self, node_name, force=False, timeout=DEFAULT_TIMEOUT):
        """Drain a node"""
        logging(f'Draining node {node_name}')
        self.host.drain_node(node_name, force, timeout)

    def add_node_label(self, node_name, key, value):
        """Add label to node"""
        logging(f'Adding label {key}={value} to node {node_name}')
        self.host.add_node_label(node_name, key, value)

    def remove_node_label(self, node_name, key):
        """Remove label from node"""
        logging(f'Removing label {key} from node {node_name}')
        self.host.remove_node_label(node_name, key)

    def get_node_capacity(self, node_name):
        """Get node capacity"""
        logging(f'Getting capacity for node {node_name}')
        return self.host.get_node_capacity(node_name)

    def get_node_resource_utilization(self, node_name):
        """Get a node's requests-based CPU/memory utilization

        Matches how the descheduler's LowNodeUtilization strategy measures nodes.
        """
        logging(f'Getting resource utilization for node {node_name}')
        return self.host.get_node_resource_utilization(node_name)

    def get_node_vms(self, node_name):
        """Get VMs on node"""
        logging(f'Getting VMs on node {node_name}')
        return self.host.get_node_vms(node_name)

    # Longhorn Node
    def get_lh_node(self, node_name):
        """Get Longhorn-specific node details"""
        return self.host.get_lh_node(node_name)

    def add_lh_node_disk_tag(self, node_name, disk_name, tag):
        """Add a tag to a Longhorn node disk"""
        return self.host.add_lh_node_disk_tag(node_name, disk_name, tag)

    def remove_lh_node_disk_tag(self, node_name, disk_name, tag):
        """Remove a tag from a Longhorn node disk"""
        return self.host.remove_lh_node_disk_tag(node_name, disk_name, tag)

    def is_lh_node_disk_tag_present(self, node_name, disk_name, tag):
        """Check if a tag exists on a Longhorn node disk"""
        lh_node = self.host.get_lh_node(node_name)
        disks = (lh_node or {}).get("spec", {}).get("disks", {})
        tags = disks.get(disk_name, {}).get("tags", [])
        return tag in tags
