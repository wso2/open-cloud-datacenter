"""
Storage Network Keywords - creates StorageNetwork() instance and delegates
Layer 3: Keyword wrappers for Robot Framework
"""
import json
import os
import sys
import time
from ipaddress import ip_address, ip_network

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa E402
from utility.utility import logging  # noqa E402
from storage_network import StorageNetwork  # noqa E402
from common_keywords import common_keywords  # noqa E402
from host_keywords import host_keywords  # noqa E402
from constant import DEFAULT_RETRY_INTERVAL, DEFAULT_TIMEOUT  # noqa E402

NETS_ANNO = "k8s.v1.cni.cncf.io/networks"
NET_STATUS_ANNO = "k8s.v1.cni.cncf.io/network-status"


class storage_network_keywords:
    """Storage Network keyword wrapper - creates StorageNetwork component and delegates"""
    def __init__(self):
        """Initialize storage network keywords with lazy loading"""
        self._storage_network = None
        self._common = None
        self._host = None

    @property
    def storage_network(self):
        """Lazy initialize storage network to allow API client setup first"""
        if self._storage_network is None:
            self._storage_network = StorageNetwork()
        return self._storage_network

    @property
    def common(self):
        if self._common is None:
            self._common = common_keywords()
        return self._common

    @property
    def host(self):
        if self._host is None:
            self._host = host_keywords()
        return self._host

    def enable_storage_network(self, vlan_id, cluster_network, ip_range,
                               share_rwx=False):
        """
        Enable the storage-network Harvester setting.

        Args:
            vlan_id: VLAN ID for the storage network
            cluster_network: Cluster network name
            ip_range: CIDR IP range for storage network
            share_rwx: If True, share storage network for RWX volumes
        """
        logging(f"Enabling storage network: vlan={vlan_id}, "
                f"cluster_network={cluster_network}, ip_range={ip_range}, "
                f"share_rwx={share_rwx}")
        # Robot Framework ${TRUE} passes as Python bool, but handle strings too
        rwx = share_rwx if isinstance(share_rwx, bool) else str(share_rwx).lower() == 'true'
        self.storage_network.enable_storage_network(
            vlan_id, cluster_network, ip_range,
            share_rwx=rwx
        )

    def disable_storage_network(self):
        """Disable the storage-network Harvester setting."""
        logging("Disabling storage network")
        self.storage_network.disable_storage_network()

    def get_storage_network_status(self):
        """
        Get the current storage-network setting status.

        Returns:
            dict: The storage-network setting data
        """
        logging("Getting storage-network status")
        return self.storage_network.get_storage_network_status()

    def _is_storage_network_configured(self, snet_data):
        """ Check storage-network is configured
        snet_data: return value of get_storage_network_status()
        """
        conditions = snet_data.get("status", {}).get("conditions", [])
        if not conditions:
            return False

        last = conditions[-1]
        return last.get("reason") == "Completed" and last.get("status") == "True"

    def is_storage_network_enabled_in_config(self):
        """ Check storage-network is enabled from config perspective
        """
        snet_data = self.get_storage_network_status()
        return self._is_storage_network_configured(snet_data) and snet_data.get("value")

    def wait_storage_network_enabled_in_config(self, timeout=DEFAULT_TIMEOUT):
        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            if self.is_storage_network_enabled_in_config():
                return
            time.sleep(DEFAULT_RETRY_INTERVAL)

        raise Exception("Timeout waiting for storage-network to be enabled on harvester setting")

    def is_storage_network_disabled_in_config(self):
        """ Check storage-network is disabled from config perspective
        """
        snet_data = self.get_storage_network_status()
        return self._is_storage_network_configured(snet_data) and not snet_data.get("value")

    def wait_storage_network_disabled_in_config(self, timeout=DEFAULT_TIMEOUT):
        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            if self.is_storage_network_disabled_in_config():
                return
            time.sleep(DEFAULT_RETRY_INTERVAL)

        raise Exception("Timeout waiting for storage-network to be disabled on harvester setting")

    def is_storage_network_enabled_on_pods(self, snet_cidr):
        """ Check storage-network is enabled from service perspective
        """
        standard_nodes = self.host.get_standard_nodes()
        imgrs = self.common.list_running_im_pods()

        # Check IM pods are Running
        if not len(imgrs) == len(standard_nodes):
            logging(
                "IM pods counts do not match standard nodes: "
                f"\nimgrs: {[imgr.metadata.name for imgr in imgrs]}"
                f"\nstandard_nodes: {standard_nodes}"
            )
            return False

        # Check IM pods have proper annotations with correct CIDR
        for imgr in imgrs:
            metadata = getattr(imgr, 'metadata', None)
            annotations = getattr(metadata, 'annotations', {}) or {}
            name = getattr(metadata, 'name', '<unknown>')
            for target_anno in [NETS_ANNO, NET_STATUS_ANNO]:
                if target_anno not in annotations:
                    logging(f"Pod {name} has no annotation {target_anno}")
                    return False

            # Check k8s.v1.cni.cncf.io/networks
            target_anno = NETS_ANNO
            try:
                nets = json.loads(annotations[target_anno])
                snet = next(n for n in nets if 'lhnet1' == n.get('interface'))
            except StopIteration:
                logging(f"Annotation {target_anno} has no interface 'lhnet1' in pod {name}")
                return False

            # Check k8s.v1.cni.cncf.io/network-status
            target_anno = NET_STATUS_ANNO
            try:
                net_statuses = json.loads(annotations[target_anno])
                snet_status = next(s for s in net_statuses if 'lhnet1' == s.get('interface'))
            except StopIteration:
                logging(f"Annotation {target_anno} has no interface 'lhnet1' in pod {name}")
                return False

            snet_ips = snet_status.get('ips', ['::1'])
            if not all(ip_address(sip) in ip_network(snet_cidr) for sip in snet_ips):
                logging(f"Dedicated IPs {snet_ips} does NOT fit {snet_cidr} in pod {name}")
                return False

            # Check network name identical in both annotations
            if f"{snet.get('namespace')}/{snet.get('name')}" != snet_status.get('name'):
                logging(
                    f"Network name is not identical between annotations "
                    f"k8s.v1.cni.cncf.io/networks and k8s.v1.cni.cncf.io/network-status "
                    f"in pod {name}"
                )
                return False

        return True

    def wait_storage_network_enabled_on_pods(self, snet_cidr, timeout=DEFAULT_TIMEOUT):
        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            if self.is_storage_network_enabled_on_pods(snet_cidr):
                return
            time.sleep(DEFAULT_RETRY_INTERVAL)

        raise Exception("Timeout waiting for storage-network to be enabled on longhorn pods")

    def is_storage_network_disabled_on_pods(self):
        """ Check storage-network is disabled from service perspective
        """
        standard_nodes = self.host.get_standard_nodes()
        imgrs = self.common.list_running_im_pods()

        # Check IM pods are Running
        if not len(imgrs) == len(standard_nodes):
            logging(
                "IM pods counts do not match standard nodes:  "
                f"\nimgrs: {[imgr.metadata.name for imgr in imgrs]}"
                f"\nstandard_nodes: {standard_nodes}"
            )
            return False

        # IM pods should not have 'k8s.v1.cni.cncf.io/networks' annotation
        for imgr in imgrs:
            metadata = getattr(imgr, 'metadata', None)
            annotations = getattr(metadata, 'annotations', {}) or {}
            name = getattr(metadata, 'name', '<unknown>')
            if NETS_ANNO in annotations:
                logging(f"Pod {name} should not have annotation {NETS_ANNO}")
                return False

        return True

    def wait_storage_network_disabled_on_pods(self, timeout=DEFAULT_TIMEOUT):
        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            if self.is_storage_network_disabled_on_pods():
                return
            time.sleep(DEFAULT_RETRY_INTERVAL)

        raise Exception("Timeout waiting for storage-network to be disabled on longhorn pods")

    def get_vlan_network_cidr(self, vlan_id, cluster_network):
        """
        Get the CIDR for a VLAN network by creating a temporary network.

        Args:
            vlan_id: VLAN ID
            cluster_network: Cluster network name

        Returns:
            str: CIDR string
        """
        logging(f"Getting VLAN network CIDR for vlan={vlan_id}")
        return self.storage_network.get_vlan_network_cidr(
            vlan_id, cluster_network
        )

    def enable_longhorn_rwx_storage_network(self):
        """Set the Longhorn endpoint-network-for-rwx-volume setting."""
        logging("Setting Longhorn RWX endpoint network")
        self.storage_network.enable_longhorn_rwx_storage_network()

    def disable_longhorn_rwx_storage_network(self):
        """Clear the Longhorn endpoint-network-for-rwx-volume setting."""
        logging("Clearing Longhorn RWX endpoint network")
        self.storage_network.disable_longhorn_rwx_storage_network()
