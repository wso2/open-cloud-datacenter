"""
Storage Network REST Implementation - Harvester REST API operations
Layer 4: Makes actual REST API calls for storage network operations
"""

import time
import json
from utility.utility import logging, get_harvester_api_client
from constant import DEFAULT_NAMESPACE
from storage_network.base import Base


class Rest(Base):
    """
    REST implementation for Storage Network operations using Harvester API
    """

    def __init__(self):
        """Initialize REST client"""
        self.harvester_api = get_harvester_api_client()

    def enable_storage_network(self, vlan_id, cluster_network, ip_range,
                               share_rwx=False):
        """Enable the storage-network Harvester setting via REST API"""
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

        code, data = self.harvester_api.settings.get("storage-network")
        if code != 200:
            raise Exception(
                f"Failed to get storage-network setting: {code}, {data}"
            )

        data["value"] = value
        path = (f"apis/{self.harvester_api.API_VERSION}"
                f"/settings/storage-network")
        resp = self.harvester_api._put(path, json=data)

        if resp.status_code not in [200, 201]:
            try:
                error_data = resp.json()
            except Exception:
                error_data = resp.text
            raise Exception(
                f"Failed to enable storage-network: "
                f"{resp.status_code}, {error_data}"
            )

        logging("Successfully enabled storage-network setting")

    def disable_storage_network(self):
        """Disable the storage-network Harvester setting via REST API"""
        logging("Disabling storage network")

        code, data = self.harvester_api.settings.get("storage-network")
        if code != 200:
            raise Exception(
                f"Failed to get storage-network setting: {code}, {data}"
            )

        data["value"] = None
        path = (f"apis/{self.harvester_api.API_VERSION}"
                f"/settings/storage-network")
        resp = self.harvester_api._put(path, json=data)

        if resp.status_code not in [200, 201]:
            try:
                error_data = resp.json()
            except Exception:
                error_data = resp.text
            raise Exception(
                f"Failed to disable storage-network: "
                f"{resp.status_code}, {error_data}"
            )

        logging("Successfully disabled storage-network setting")

    def get_storage_network_status(self):
        """Get the current storage-network setting status"""
        logging("Getting storage-network status")

        code, data = self.harvester_api.settings.get("storage-network")
        if code != 200:
            raise Exception(
                f"Failed to get storage-network setting: {code}, {data}"
            )

        return data

    def get_vlan_network_cidr(self, vlan_id, cluster_network):
        """Get CIDR by creating a temp VLAN network and reading its route"""
        logging(f"Getting VLAN network CIDR for vlan={vlan_id}")

        temp_name = f"temp-snet-{int(time.time())}"
        self._create_temp_vlan_network(temp_name, vlan_id, cluster_network)

        try:
            end_time = time.time() + 120
            while time.time() < end_time:
                code, data = self.harvester_api.get(
                    f"v1/k8s.cni.cncf.io.network-attachment-definitions"
                    f"/{DEFAULT_NAMESPACE}/{temp_name}"
                )
                if code == 200:
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
        path = ("apis/longhorn.io/v1beta2/namespaces/longhorn-system"
                "/settings/storage-network")
        code, data = self.harvester_api._get(path)

        if hasattr(code, 'status_code'):
            resp = code
            if resp.status_code != 200:
                raise Exception(
                    f"Failed to get Longhorn storage-network setting: {resp.status_code}"
                )
            data = resp.json()
        else:
            if code != 200:
                raise Exception(
                    f"Failed to get Longhorn storage-network setting: {code}, {data}"
                )

        nad_ref = data.get("value", "").strip()
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

        path = ("apis/longhorn.io/v1beta2/namespaces/longhorn-system"
                "/settings/endpoint-network-for-rwx-volume")
        code, data = self.harvester_api._get(path)

        if hasattr(code, 'status_code'):
            resp = code
            if resp.status_code != 200:
                raise Exception(
                    f"Failed to get Longhorn RWX endpoint network setting: {resp.status_code}"
                )
            data = resp.json()
            data["value"] = nad_ref
            resp = self.harvester_api._put(path, json=data)
            if resp.status_code not in [200, 201]:
                raise Exception(
                    f"Failed to set Longhorn RWX endpoint network: "
                    f"{resp.status_code}"
                )
        else:
            if code != 200:
                raise Exception(
                    f"Failed to get Longhorn RWX endpoint network setting: {code}, {data}"
                )
            data["value"] = nad_ref
            resp = self.harvester_api._put(path, json=data)
            if resp.status_code not in [200, 201]:
                raise Exception(
                    f"Failed to set Longhorn RWX endpoint network: "
                    f"{resp.status_code}"
                )

        logging("Successfully set Longhorn RWX endpoint network")

    def disable_longhorn_rwx_storage_network(self):
        """Clear the Longhorn endpoint-network-for-rwx-volume setting"""
        logging("Clearing Longhorn endpoint-network-for-rwx-volume")

        path = ("apis/longhorn.io/v1beta2/namespaces/longhorn-system"
                "/settings/endpoint-network-for-rwx-volume")
        code, data = self.harvester_api._get(path)

        if hasattr(code, 'status_code'):
            resp = code
            if resp.status_code != 200:
                raise Exception(
                    f"Failed to get Longhorn RWX endpoint network setting: {resp.status_code}"
                )
            data = resp.json()
            data["value"] = ""
            resp = self.harvester_api._put(path, json=data)
            if resp.status_code not in [200, 201]:
                raise Exception(
                    f"Failed to clear Longhorn RWX endpoint network: "
                    f"{resp.status_code}"
                )
        else:
            if code != 200:
                raise Exception(
                    f"Failed to get Longhorn RWX endpoint network setting: {code}, {data}"
                )
            data["value"] = ""
            resp = self.harvester_api._put(path, json=data)
            if resp.status_code not in [200, 201]:
                raise Exception(
                    f"Failed to clear Longhorn RWX endpoint network: "
                    f"{resp.status_code}"
                )

    # Private helpers for temp VLAN network used by get_vlan_network_cidr
    def _create_temp_vlan_network(self, name, vlan_id, cluster_network):
        """Create a temporary VLAN network for CIDR discovery"""
        logging(f"Creating temp VLAN network: {name}")

        code, data = self.harvester_api.post(
            f"v1/k8s.cni.cncf.io.network-attachment-definitions"
            f"/{DEFAULT_NAMESPACE}",
            data={
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
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create temp VLAN network: {code}, {data}"
            )

    def _delete_temp_vlan_network(self, name):
        """Delete a temporary VLAN network"""
        logging(f"Deleting temp VLAN network: {name}")
        try:
            self.harvester_api.delete(
                f"v1/k8s.cni.cncf.io.network-attachment-definitions"
                f"/{DEFAULT_NAMESPACE}/{name}"
            )
        except Exception as e:
            logging(f"Warning: Failed to delete temp network {name}: {e}",
                    level="WARNING")
