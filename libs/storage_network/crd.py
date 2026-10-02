"""
Storage Network CRD Implementation - Kubernetes API operations
Layer 4: Makes actual kubectl/K8s API calls for storage network operations
"""

import time
import subprocess
import json
from kubernetes import client
from kubernetes.client.rest import ApiException
from constant import DEFAULT_NAMESPACE
from utility.utility import logging
from storage_network.base import Base


class CRD(Base):
    """
    CRD implementation for Storage Network operations using Kubernetes API
    """

    def __init__(self):
        """Initialize Kubernetes clients"""
        self.custom_api = client.CustomObjectsApi()

    def _run_kubectl(self, args):
        """Run kubectl command and return output.

        Args:
            args: List of kubectl arguments

        Returns:
            tuple: (return_code, stdout, stderr)
        """
        cmd = ["kubectl", "--insecure-skip-tls-verify"] + args
        logging(f"Running kubectl command: {' '.join(cmd)}", level="DEBUG")
        result = subprocess.run(  # nosec B603
            cmd, capture_output=True, text=True, input=None
        )
        return result.returncode, result.stdout, result.stderr

    def enable_storage_network(self, vlan_id, cluster_network, ip_range,
                               share_rwx=False):
        """Enable the storage-network Harvester setting via kubectl"""
        logging(f"Enabling storage network: vlan={vlan_id}, "
                f"cluster_network={cluster_network}, ip_range={ip_range}, "
                f"share_rwx={share_rwx}")

        value_dict = {
            "vlan": int(vlan_id),
            "clusterNetwork": cluster_network,
            "range": ip_range
        }
        if share_rwx:
            value_dict["share-storage-network"] = True

        value = json.dumps(value_dict)

        patch_data = json.dumps({"value": value})

        cmd = ["kubectl", "patch", "settings.harvesterhci.io",
               "storage-network", "--type=merge", "-p", patch_data,
               "--insecure-skip-tls-verify"]

        result = subprocess.run(  # nosec B603
            cmd, capture_output=True, text=True, input=None
        )

        if result.returncode != 0:
            raise Exception(
                f"Failed to enable storage-network: {result.stderr}"
            )

        logging("Successfully enabled storage-network setting")

    def disable_storage_network(self):
        """Disable the storage-network Harvester setting via kubectl"""
        logging("Disabling storage network")

        patch_data = json.dumps({"value": None})

        cmd = ["kubectl", "patch", "settings.harvesterhci.io",
               "storage-network", "--type=merge", "-p", patch_data,
               "--insecure-skip-tls-verify"]

        result = subprocess.run(  # nosec B603
            cmd, capture_output=True, text=True, input=None
        )

        if result.returncode != 0:
            raise Exception(
                f"Failed to disable storage-network: {result.stderr}"
            )

        logging("Successfully disabled storage-network setting")

    def get_storage_network_status(self):
        """Get the current storage-network setting status via kubectl"""
        logging("Getting storage-network status")

        rc, stdout, stderr = self._run_kubectl(
            ["get", "settings.harvesterhci.io", "storage-network",
             "-o", "json"]
        )

        if rc != 0:
            raise Exception(
                f"Failed to get storage-network setting: {stderr}"
            )

        return json.loads(stdout)

    def get_vlan_network_cidr(self, vlan_id, cluster_network):
        """Get CIDR by creating a temp VLAN network and reading its route"""
        logging(f"Getting VLAN network CIDR for vlan={vlan_id}")

        temp_name = f"temp-snet-{int(time.time())}"
        self._create_temp_vlan_network(temp_name, vlan_id, cluster_network)

        try:
            end_time = time.time() + 120
            while time.time() < end_time:
                rc, stdout, stderr = self._run_kubectl(
                    ["get",
                     "network-attachment-definitions.k8s.cni.cncf.io",
                     temp_name, "-n", DEFAULT_NAMESPACE, "-o", "json"]
                )
                if rc == 0:
                    data = json.loads(stdout)
                    annotations = data.get("metadata", {}).get(
                        "annotations", {}
                    )
                    route_str = annotations.get(
                        "network.harvesterhci.io/route", ""
                    )
                    if route_str:
                        route = json.loads(route_str)
                        cidr = route.get("cidr", "")
                        if cidr:
                            logging(f"Got CIDR from temp network: {cidr}")
                            return cidr
                time.sleep(5)

            raise Exception(
                f"Timeout getting CIDR from temp VLAN network {temp_name}"
            )
        finally:
            self._delete_temp_vlan_network(temp_name)

    def _get_longhorn_storage_network_nad(self):
        """Read the NAD reference from Longhorn's storage-network setting.

        After Harvester enables storage-network, it populates Longhorn's
        storage-network setting with the NAD (e.g. harvester-system/storagenetwork-xxxxx).
        We reuse the same NAD for the RWX endpoint network.

        Returns:
            str: NAD reference in namespace/name format
        """
        rc, stdout, stderr = self._run_kubectl(
            ["get", "settings.longhorn.io", "storage-network",
             "-n", "longhorn-system",
             "-o", "jsonpath={.value}"]
        )
        if rc != 0:
            raise Exception(
                f"Failed to get Longhorn storage-network setting: {stderr}"
            )
        nad_ref = stdout.strip()
        if not nad_ref:
            raise Exception(
                "Longhorn storage-network setting is empty. "
                "Ensure Harvester storage-network is enabled first."
            )
        logging(f"Got Longhorn storage-network NAD: {nad_ref}")
        return nad_ref

    def enable_longhorn_rwx_storage_network(self):
        """Set the Longhorn endpoint-network-for-rwx-volume setting
        to the same NAD used by Longhorn's storage-network setting."""
        nad_ref = self._get_longhorn_storage_network_nad()
        logging(f"Setting Longhorn endpoint-network-for-rwx-volume to {nad_ref}")

        patch_data = json.dumps({"value": nad_ref})

        cmd = ["kubectl", "patch", "settings.longhorn.io",
               "endpoint-network-for-rwx-volume",
               "-n", "longhorn-system",
               "--type=merge", "-p", patch_data,
               "--insecure-skip-tls-verify"]

        result = subprocess.run(  # nosec B603
            cmd, capture_output=True, text=True, input=None
        )

        if result.returncode != 0:
            raise Exception(
                f"Failed to set Longhorn RWX endpoint network: {result.stderr}"
            )

        logging("Successfully set Longhorn RWX endpoint network")

    def disable_longhorn_rwx_storage_network(self):
        """Clear the Longhorn endpoint-network-for-rwx-volume setting"""
        logging("Clearing Longhorn endpoint-network-for-rwx-volume")

        patch_data = json.dumps({"value": ""})

        cmd = ["kubectl", "patch", "settings.longhorn.io",
               "endpoint-network-for-rwx-volume",
               "-n", "longhorn-system",
               "--type=merge", "-p", patch_data,
               "--insecure-skip-tls-verify"]

        result = subprocess.run(  # nosec B603
            cmd, capture_output=True, text=True, input=None
        )

        if result.returncode != 0:
            raise Exception(
                f"Failed to clear Longhorn RWX endpoint network: {result.stderr}"
            )

    # Private helpers for temp VLAN network used by get_vlan_network_cidr
    def _create_temp_vlan_network(self, name, vlan_id, cluster_network):
        """Create a temporary VLAN network for CIDR discovery"""
        logging(f"Creating temp VLAN network: {name}")

        network_spec = {
            "apiVersion": "k8s.cni.cncf.io/v1",
            "kind": "NetworkAttachmentDefinition",
            "metadata": {
                "name": name,
                "namespace": DEFAULT_NAMESPACE,
                "annotations": {
                    "network.harvesterhci.io/route": '{"mode":"auto"}'
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

        try:
            self.custom_api.create_namespaced_custom_object(
                group="k8s.cni.cncf.io",
                version="v1",
                namespace=DEFAULT_NAMESPACE,
                plural="network-attachment-definitions",
                body=network_spec
            )
        except ApiException as e:
            if e.status != 409:  # Ignore already-exists
                raise Exception(
                    f"Failed to create temp VLAN network: {e}"
                )

    def _delete_temp_vlan_network(self, name):
        """Delete a temporary VLAN network"""
        logging(f"Deleting temp VLAN network: {name}")
        try:
            self.custom_api.delete_namespaced_custom_object(
                group="k8s.cni.cncf.io",
                version="v1",
                namespace=DEFAULT_NAMESPACE,
                plural="network-attachment-definitions",
                name=name
            )
        except ApiException as e:
            if e.status != 404:
                logging(f"Warning: Failed to delete temp network {name}: {e}",
                        level="WARNING")
