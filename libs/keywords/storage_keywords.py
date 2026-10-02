""" Storage Keyword Wrapper

Layer 3: Keyword wrapper (NO direct API calls)
"""

import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging  # noqa E402
from blockdevice import Blockdevice  # noqa E402
from storageclass import StorageClass  # noqa E402
from host_keywords import host_keywords  # noqa E402


class storage_keywords:
    """Storage keyword wrapper"""

    def __init__(self):
        """Initialize storage keywords with lazy loading"""
        self._blockdevice = None
        self._storageclass = None
        self._host = None

    @property
    def blockdevice(self):
        """Lazy initialize blockdevice to allow API client setup first"""
        if self._blockdevice is None:
            self._blockdevice = Blockdevice()
        return self._blockdevice

    @property
    def host(self):
        """Lazy initialize host_keywords to allow API client setup first"""
        if self._host is None:
            self._host = host_keywords()
        return self._host

    @property
    def storageclass(self):
        """Lazy initialize storageclass to allow API client setup first"""
        if self._storageclass is None:
            self._storageclass = StorageClass()
        return self._storageclass

    def list_blockdevices(self, namespace):
        return self.blockdevice.list(namespace)

    def get_blockdevice(self, name, namespace):
        return self.blockdevice.get(name, namespace)

    def list_nvme_blockdevices(self, namespace):
        blockdevices = self.list_blockdevices(namespace)
        nvme_blockdevices = []

        logging(f"Finding NVMe blockdevices in namespace {namespace}")
        for blockdevice in blockdevices:
            try:
                device_status = blockdevice.get("status", {}).get("deviceStatus", {})
                details = device_status.get("details", {})
                storage_controller = str(details.get("storageController", "")).lower()
                dev_path = str(device_status.get("devPath", "")).lower()
                bus_path = str(details.get("busPath", "")).lower()

                is_nvme = (
                    "nvme" in storage_controller or
                    "nvme" in dev_path or
                    "nvme" in bus_path
                )
            except Exception:
                is_nvme = False
                logging(
                    f"Unable to determine NVMe for blockdevice {blockdevice}", level="WARNING"
                )

            if is_nvme:
                nvme_blockdevices.append(blockdevice)

        logging(f"Found {len(nvme_blockdevices)} NVMe blockdevices in namespace {namespace}")
        return nvme_blockdevices

    def pick_unprovisioned_nvme_disks(self, namespace: str) -> dict:
        standard_node_names = self.host.get_standard_nodes()
        logging(f"Standard nodes: {standard_node_names}")

        large_disks = self.list_nvme_blockdevices(namespace)
        unprovisioned_disk_by_node = {}
        for blockdevice in large_disks:
            node_name = blockdevice.get("spec", {}).get("nodeName")
            if not node_name or node_name not in standard_node_names:
                continue
            if node_name in unprovisioned_disk_by_node:
                continue

            disk_name = blockdevice.get("metadata", {}).get("name")
            if not disk_name:
                continue

            disk_provision_phase = blockdevice.get("status", {}).get("provisionPhase")
            if disk_provision_phase != "Unprovisioned":
                continue

            unprovisioned_disk_by_node[node_name] = disk_name

        logging(f"Picked unprovisioned NVMe disks: {unprovisioned_disk_by_node}")
        return unprovisioned_disk_by_node

    def provision_longhorn_storage(self, disk_name: str, engine_version: str, namespace: str):
        self.blockdevice.provision_longhorn_storage(disk_name, engine_version, namespace)

    def is_blockdevice_provisioned(self, name: str, namespace: str) -> bool:
        blockdevice = self.get_blockdevice(name, namespace)
        provision_phase = blockdevice and blockdevice.get("status", {}).get("provisionPhase")

        if provision_phase == "Provisioned":
            return True
        logging(f"Blockdevice {namespace}/{name} is not Provisioned but {provision_phase}")
        return False

    def get_lh_node(self, node_name: str):
        return self.host.get_lh_node(node_name)

    def get_lh_node_disk_status(self, node_name: str, disk_name: str):
        lh_node = self.get_lh_node(node_name)
        disk_status = lh_node.get("status", {}).get("diskStatus", {}).get(disk_name, {})
        return disk_status

    def get_lh_node_disk_status_condition(self, node_name, disk_name, condition_type):
        disk_status = self.get_lh_node_disk_status(node_name, disk_name)
        for condition in disk_status.get("conditions", []):
            if condition.get("type") == condition_type:
                return condition.get("status")

    def create_storageclass(self, name, data_engine, number_of_replicas, disk_selector):
        return self.storageclass.create(
            name, data_engine, number_of_replicas, disk_selector
        )

    def delete_storageclass(self, name):
        return self.storageclass.delete(name)

    def get_storageclass(self, name):
        return self.storageclass.get(name)
