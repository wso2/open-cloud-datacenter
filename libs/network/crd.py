"""
Network CRD Implementation - Kubernetes API operations
Layer 4: Makes actual kubectl/K8s API calls for network operations
"""

import json
from kubernetes.client.rest import ApiException
from crd import (
    create_cr, create_cluster_cr,
    delete_cr, delete_cluster_cr,
    list_cr, list_cluster_cr,
    wait_for_cr_deleted, wait_for_cluster_cr_deleted,
    get_cluster_cr,
    wait_for_cluster_cr_condition,
)
from constant import DEFAULT_NAMESPACE, LABEL_TEST, LABEL_TEST_VALUE, DEFAULT_TIMEOUT_SHORT
from utility.utility import logging
from network.base import Base


class CRD(Base):
    """
    CRD implementation for Network operations using Kubernetes API
    """

    # Cluster Network Operations
    def create_cluster_network(self, name):
        """Create cluster network"""
        logging(f"Creating cluster network: {name}")

        spec = {
            "apiVersion": "network.harvesterhci.io/v1beta1",
            "kind": "ClusterNetwork",
            "metadata": {
                "name": name,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                },
            }
        }

        try:
            create_cluster_cr(
                group="network.harvesterhci.io",
                version="v1beta1",
                plural="clusternetworks",
                body=spec
            )
            logging(f"Created cluster network: {name}")
            return spec
        except ApiException as e:
            if e.status == 409:
                logging(f"Cluster network {name} already exists")
                return spec
            raise Exception(
                f"Failed to create cluster network: {e}"
            )

    def delete_cluster_network(self, name, wait=False):
        """Delete cluster network"""
        logging(f"Deleting cluster network: {name}")
        try:
            delete_cluster_cr(
                group="network.harvesterhci.io",
                version="v1beta1",
                plural="clusternetworks",
                name=name
            )
            if wait:
                wait_for_cluster_cr_deleted(
                    group="network.harvesterhci.io",
                    version="v1beta1",
                    plural="clusternetworks",
                    name=name,
                    timeout=DEFAULT_TIMEOUT_SHORT
                )
            logging(f"Deleted cluster network: {name}")
        except ApiException as e:
            if e.status != 404:
                raise Exception(
                    f"Failed to delete cluster network: {e}"
                )

    def list_cluster_networks(self, label_selector=None):
        return list_cluster_cr(
            group="network.harvesterhci.io",
            version="v1beta1",
            plural="clusternetworks",
            label_selector=label_selector
        ).get("items", [])

    def cleanup_cluster_networks(self):
        logging('Cleaning up test cluster networks')
        try:
            cnets = self.list_cluster_networks(label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}")
            for cnet in cnets:
                name = cnet['metadata']['name']
                try:
                    logging(f'Deleting test cluster networks: {name}')
                    self.delete_cluster_network(name, wait=True)
                except Exception as e:
                    logging(f'Error deleting cluster networks {name}: {e}', 'WARNING')
        except Exception as e:
            logging(f'Error during cluster networks cleanup: {e}', 'WARNING')

    def create_vlan_config(self, name, cluster_network, nic):
        """Create VLAN config to bind NIC to cluster network"""
        logging(f"Creating VLAN config: {name} "
                f"(nic={nic}, cluster_network={cluster_network})")

        spec = {
            "apiVersion": "network.harvesterhci.io/v1beta1",
            "kind": "VlanConfig",
            "metadata": {
                "name": name,
                "labels": {
                    "network.harvesterhci.io/clusternetwork": cluster_network,
                    LABEL_TEST: LABEL_TEST_VALUE
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
            }
        }

        try:
            create_cluster_cr(
                group="network.harvesterhci.io",
                version="v1beta1",
                plural="vlanconfigs",
                body=spec
            )
            logging(f"Created VLAN config: {name}")
            return spec
        except ApiException as e:
            if e.status == 409:
                logging(f"VLAN config {name} already exists")
                return spec
            raise Exception(
                f"Failed to create VLAN config: {e}"
            )

    def delete_vlan_config(self, name, wait=False):
        """Delete VLAN config"""
        logging(f"Deleting VLAN config: {name}")
        try:
            delete_cluster_cr(
                group="network.harvesterhci.io",
                version="v1beta1",
                plural="vlanconfigs",
                name=name
            )
            if wait:
                wait_for_cluster_cr_deleted(
                    group="network.harvesterhci.io",
                    version="v1beta1",
                    plural="vlanconfigs",
                    name=name,
                    timeout=DEFAULT_TIMEOUT_SHORT
                )
            logging(f"Deleted VLAN config: {name}")
        except ApiException as e:
            if e.status != 404:
                raise Exception(
                    f"Failed to delete VLAN config: {e}"
                )

    def list_vlan_configs(self, label_selector=None):
        return list_cluster_cr(
            group="network.harvesterhci.io",
            version="v1beta1",
            plural="vlanconfigs",
            label_selector=label_selector
        ).get("items", [])

    def cleanup_vlan_configs(self):
        logging('Cleaning up test vlan configs')
        try:
            vlan_configs = self.list_vlan_configs(
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )
            for vlan_config in vlan_configs:
                name = vlan_config['metadata']['name']
                try:
                    logging(f'Deleting test vlan configs: {name}')
                    self.delete_vlan_config(name, wait=True)
                except Exception as e:
                    logging(f'Error deleting vlan configs {name}: {e}', 'WARNING')
        except Exception as e:
            logging(f'Error during vlan configs cleanup: {e}', 'WARNING')

    def wait_for_cluster_network_ready(self, name, timeout=120):
        """Wait for cluster network to become ready"""
        logging(f"Waiting for cluster network {name} to be ready")
        wait_for_cluster_cr_condition(
            group="network.harvesterhci.io",
            version="v1beta1",
            plural="clusternetworks",
            name=name,
            condition_type="ready",
            condition_status="True",
            timeout=timeout
        )
        logging(f"Cluster network {name} is ready")
        return get_cluster_cr(
            group="network.harvesterhci.io",
            version="v1beta1",
            plural="clusternetworks",
            name=name
        )

    # VLAN Network Operations
    def create_vlan_network(self, name, vlan_id, cluster_network):
        """Create VLAN network"""
        logging(f"Creating VLAN network: {name}")

        network_spec = {
            "apiVersion": "k8s.cni.cncf.io/v1",
            "kind": "NetworkAttachmentDefinition",
            "metadata": {
                "name": name,
                "namespace": DEFAULT_NAMESPACE,
                "annotations": {
                    "network.harvesterhci.io/route":
                        '{"mode":"auto"}'
                },
                "labels": {
                    "network.harvesterhci.io/clusternetwork": cluster_network,
                    "network.harvesterhci.io/type": "L2VlanNetwork",
                    LABEL_TEST: LABEL_TEST_VALUE
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

        try:
            create_cr(
                group="k8s.cni.cncf.io",
                version="v1",
                namespace=DEFAULT_NAMESPACE,
                plural="network-attachment-definitions",
                body=network_spec
            )
            logging(f"Created VLAN network: {name}")
            return network_spec
        except ApiException as e:
            if e.status == 409:
                logging(f"VLAN network {name} already exists")
                return network_spec
            raise Exception(
                f"Failed to create VLAN network: {e}"
            )

    def delete_vlan_network(self, name, wait=False):
        """Delete VLAN network"""
        logging(f"Deleting VLAN network: {name}")
        try:
            delete_cr(
                group="k8s.cni.cncf.io",
                version="v1",
                namespace=DEFAULT_NAMESPACE,
                plural="network-attachment-definitions",
                name=name
            )
            if wait:
                wait_for_cr_deleted(
                    group="k8s.cni.cncf.io",
                    version="v1",
                    namespace=DEFAULT_NAMESPACE,
                    plural="network-attachment-definitions",
                    name=name,
                    timeout=DEFAULT_TIMEOUT_SHORT
                )
            logging(f"Deleted VLAN network: {name}")
        except ApiException as e:
            if e.status != 404:
                raise Exception(
                    f"Failed to delete VLAN network: {e}"
                )

    def list_vlan_networks(self, label_selector=None):
        return list_cr(
            group="k8s.cni.cncf.io",
            version="v1",
            namespace=DEFAULT_NAMESPACE,
            plural="network-attachment-definitions",
            label_selector=label_selector
        ).get("items", [])

    def cleanup_vlan_networks(self):
        logging('Cleaning up test vlan networks')
        try:
            vnets = self.list_vlan_networks(label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}")
            for vnet in vnets:
                name = vnet['metadata']['name']
                try:
                    logging(f'Deleting test vlan network: {name}')
                    self.delete_vlan_network(name, wait=True)
                except Exception as e:
                    logging(f'Error deleting vlan network {name}: {e}', 'WARNING')
        except Exception as e:
            logging(f'Error during vlan network cleanup: {e}', 'WARNING')

    # IP Pool Operations
    def get_ip_pool(self, name):
        """Get IP pool by name, returns None if not found"""
        lb_group = "loadbalancer.harvesterhci.io"
        lb_version = "v1beta1"
        try:
            return get_cluster_cr(
                group=lb_group,
                version=lb_version,
                plural="ippools",
                name=name
            )
        except ApiException as e:
            if e.status == 404:
                return None
            raise Exception(f"Failed to get IP pool: {e}")

    def create_ip_pool(self, name, subnet, start_ip, end_ip, network_id,
                       gateway):
        """Create IP pool, reusing existing pool with the same name"""
        logging(f"Creating IP pool: {name}")

        existing = self.get_ip_pool(name)
        if existing:
            logging(f"IP pool {name} already exists, reusing")
            return existing

        lb_group = "loadbalancer.harvesterhci.io"
        lb_version = "v1beta1"

        ippool_spec = {
            "apiVersion": f"{lb_group}/{lb_version}",
            "kind": "IPPool",
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

        try:
            create_cluster_cr(
                group=lb_group,
                version=lb_version,
                plural="ippools",
                body=ippool_spec
            )
            logging(f"Created IP pool: {name}")
            return ippool_spec
        except ApiException as e:
            if e.status == 409:
                logging(f"IP pool {name} already exists")
                return ippool_spec
            raise Exception(
                f"Failed to create IP pool: {e}"
            )

    def delete_ip_pool(self, name):
        """Delete IP pool"""
        logging(f"Deleting IP pool: {name}")

        lb_group = "loadbalancer.harvesterhci.io"
        lb_version = "v1beta1"

        try:
            delete_cluster_cr(
                group=lb_group,
                version=lb_version,
                plural="ippools",
                name=name
            )
            logging(f"Deleted IP pool: {name}")
        except ApiException as e:
            if e.status != 404:
                raise Exception(
                    f"Failed to delete IP pool: {e}"
                )
