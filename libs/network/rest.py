"""
Network REST Implementation - Harvester REST API operations
Layer 4: Makes actual REST API calls for network operations
"""

import time
import json
from utility.utility import logging, get_harvester_api_client
from constant import DEFAULT_NAMESPACE
from network.base import Base


class Rest(Base):
    """
    REST implementation for Network operations using Harvester API
    """

    def __init__(self):
        """Initialize REST client"""
        self.harvester_api = get_harvester_api_client()

    # Cluster Network Operations
    def create_cluster_network(self, name):
        """Create cluster network"""
        logging(f"Creating cluster network: {name}")

        code, data = self.harvester_api.post(
            "v1/harvester/network.harvesterhci.io.clusternetworks",
            data={
                "apiVersion": "network.harvesterhci.io/v1beta1",
                "kind": "ClusterNetwork",
                "metadata": {
                    "name": name
                },
                "type": "network.harvesterhci.io.clusternetwork"
            }
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create cluster network: {code}, {data}"
            )

        logging(f"Created cluster network: {name}")
        return data

    def delete_cluster_network(self, name):
        """Delete cluster network"""
        logging(f"Deleting cluster network: {name}")

        code, data = self.harvester_api.delete(
            f"v1/harvester/network.harvesterhci.io.clusternetworks/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(
                f"Failed to delete cluster network: {code}, {data}"
            )

        logging(f"Deleted cluster network: {name}")

    def cleanup_cluster_networks(self):
        pass

    def create_vlan_config(self, name, cluster_network, nic):
        """Create VLAN config to bind NIC to cluster network"""
        logging(f"Creating VLAN config: {name} "
                f"(nic={nic}, cluster_network={cluster_network})")

        code, data = self.harvester_api.post(
            "v1/harvester/network.harvesterhci.io.vlanconfigs",
            data={
                "apiVersion": "network.harvesterhci.io/v1beta1",
                "kind": "VlanConfig",
                "metadata": {
                    "name": name,
                    "labels": {
                        "network.harvesterhci.io/clusternetwork":
                            cluster_network
                    }
                },
                "spec": {
                    "clusterNetwork": cluster_network,
                    "uplink": {
                        "bondOptions": {
                            "miimon": -1,
                            "mode": "active-backup"
                        },
                        "linkAttributes": {
                            "txQLen": -1
                        },
                        "nics": [nic]
                    }
                },
                "type": "network.harvesterhci.io.vlanconfig"
            }
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create VLAN config: {code}, {data}"
            )

        logging(f"Created VLAN config: {name}")
        return data

    def delete_vlan_config(self, name):
        """Delete VLAN config"""
        logging(f"Deleting VLAN config: {name}")

        code, data = self.harvester_api.delete(
            f"v1/harvester/network.harvesterhci.io.vlanconfigs/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(
                f"Failed to delete VLAN config: {code}, {data}"
            )

        logging(f"Deleted VLAN config: {name}")

    def cleanup_vlan_configs(self):
        pass

    def wait_for_cluster_network_ready(self, name, timeout=120):
        """Wait for cluster network to become ready"""
        logging(f"Waiting for cluster network {name} to be ready")
        deadline = time.time() + int(timeout)

        while time.time() < deadline:
            try:
                code, data = self.harvester_api.get(
                    "v1/harvester/"
                    f"network.harvesterhci.io.clusternetworks/{name}"
                )
                if code == 200:
                    conditions = data.get("status", {}).get(
                        "conditions", []
                    )
                    for cond in conditions:
                        if (cond.get("type") == "ready"
                                and cond.get("status") == "True"):
                            logging(
                                f"Cluster network {name} is ready"
                            )
                            return data
            except Exception as e:
                logging(
                    f"Error polling cluster network {name}: {e}",
                    level="WARNING"
                )
            time.sleep(5)

        raise Exception(
            f"Cluster network {name} not ready within {timeout}s"
        )

    # VLAN Network Operations
    def create_vlan_network(self, name, vlan_id, cluster_network):
        """Create VLAN network"""
        logging(f"Creating VLAN network: {name}")

        code, data = self.harvester_api.post(
            "v1/k8s.cni.cncf.io.network-attachment-definitions"
            f"/{DEFAULT_NAMESPACE}",
            data={
                "metadata": {
                    "name": name,
                    "namespace": DEFAULT_NAMESPACE,
                    "annotations": {
                        "network.harvesterhci.io/route":
                            '{"mode":"auto"}'
                    },
                    "labels": {
                        "network.harvesterhci.io/clusternetwork":
                            cluster_network,
                        "network.harvesterhci.io/type": "L2VlanNetwork"
                    }
                },
                "spec": {
                    "config": json.dumps({
                        "cniVersion": "0.3.1",
                        "name": name,
                        "type": "bridge",
                        "bridge": f"{cluster_network}-br",
                        "promiscMode": True,
                        "vlan": int(vlan_id),
                        "ipam": {}
                    })
                }
            }
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create VLAN network: {code}, {data}"
            )

        logging(f"Created VLAN network: {name}")
        return data

    def delete_vlan_network(self, name):
        """Delete VLAN network"""
        logging(f"Deleting VLAN network: {name}")

        code, data = self.harvester_api.delete(
            "v1/k8s.cni.cncf.io.network-attachment-definitions"
            f"/{DEFAULT_NAMESPACE}/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(
                f"Failed to delete VLAN network: {code}, {data}"
            )

        logging(f"Deleted VLAN network: {name}")

    def cleanup_vlan_networks(self):
        pass

    # IP Pool Operations
    def get_ip_pool(self, name):
        """Get IP pool by name, returns None if not found"""
        logging(f"Getting IP pool: {name}")
        code, data = self.harvester_api.get(
            "v1/harvester/"
            f"loadbalancer.harvesterhci.io.ippools/{name}"
        )
        if code == 404:
            return None
        if code not in [200, 201]:
            raise Exception(
                f"Failed to get IP pool: {code}, {data}"
            )
        return data

    def create_ip_pool(self, name, subnet, start_ip, end_ip, network_id,
                       gateway):
        """Create IP pool, reusing existing pool with the same name"""
        logging(f"Creating IP pool: {name}")

        existing = self.get_ip_pool(name)
        if existing:
            logging(f"IP pool {name} already exists, reusing")
            return existing

        code, data = self.harvester_api.post(
            "v1/harvester/loadbalancer.harvesterhci.io.ippools",
            data={
                "type": "loadbalancer.harvesterhci.io.ippool",
                "metadata": {
                    "name": name
                },
                "spec": {
                    "ranges": [{
                        "subnet": subnet,
                        "rangeStart": start_ip,
                        "rangeEnd": end_ip,
                        "gateway": gateway,
                        "type": "range" if start_ip or end_ip
                        else "cidr"
                    }],
                    "selector": {
                        "network": network_id,
                        "scope": [{
                            "namespace": "*",
                            "project": "*",
                            "guestCluster": "*"
                        }]
                    }
                }
            }
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create IP pool: {code}, {data}"
            )

        logging(f"Created IP pool: {name}")
        return data

    def delete_ip_pool(self, name):
        """Delete IP pool"""
        logging(f"Deleting IP pool: {name}")

        code, data = self.harvester_api.delete(
            "v1/harvester/"
            f"loadbalancer.harvesterhci.io.ippools/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(
                f"Failed to delete IP pool: {code}, {data}"
            )

        logging(f"Deleted IP pool: {name}")
