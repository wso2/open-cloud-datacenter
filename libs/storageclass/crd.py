""" StorageClass Component: CRD Implementation

Layer 4: Component and its implementation
"""

from kubernetes import client
from kubernetes.client.rest import ApiException
from utility.utility import logging
from constant import LABEL_TEST, LABEL_TEST_VALUE
from .base import Base


class CRD(Base):
    """CRD implementation for StorageClass operations using Kubernetes API"""

    def __init__(self):
        super().__init__()
        self.custom_api = client.CustomObjectsApi()
        self.common_parameters = {
            "group": "storage.k8s.io",
            "version": "v1",
            "plural": "storageclasses"
        }

    def _normalize_disk_selector(self, disk_selector):
        if disk_selector is None:
            return ""
        parts = [part.strip() for part in str(disk_selector).split(",")]
        parts = [part for part in parts if part]
        return ",".join(parts)

    def create(self, name, data_engine, number_of_replicas, disk_selector):
        if self.get(name):
            raise Exception(f"StorageClass {name} already exists")

        disk_selector_value = self._normalize_disk_selector(disk_selector)
        body = {
            "apiVersion": "storage.k8s.io/v1",
            "kind": "StorageClass",
            "metadata": {
                "name": name,
                "annotations": {
                    "cdi.harvesterhci.io/storageProfileVolumeSnapshotClass": "longhorn-snapshot"
                },
                "labels": {
                        LABEL_TEST: LABEL_TEST_VALUE
                }
            },
            "provisioner": "driver.longhorn.io",
            "allowVolumeExpansion": True,
            "reclaimPolicy": "Delete",
            "volumeBindingMode": "Immediate",
            "parameters": {
                "numberOfReplicas": str(number_of_replicas),
                "staleReplicaTimeout": "30",
                "diskSelector": disk_selector_value,
                "encrypted": "false",
                "migratable": "true",
                "dataEngine": str(data_engine)
            }
        }

        try:
            return self.custom_api.create_cluster_custom_object(
                **self.common_parameters,
                body=body
            )
        except ApiException as e:
            raise Exception(f"Failed to create StorageClass {name}: {e}")

    def delete(self, name):
        try:
            self.custom_api.delete_cluster_custom_object(
                **self.common_parameters,
                name=name
            )
            logging(f"Deleted StorageClass {name}")
        except ApiException as e:
            if e.status == 404:
                return False
            raise Exception(f"Failed to delete StorageClass {name}: {e}")
        return True

    def get(self, name):
        try:
            return self.custom_api.get_cluster_custom_object(
                **self.common_parameters,
                name=name
            )
        except ApiException as e:
            if e.status == 404:
                return None
            raise Exception(f"Failed to get StorageClass {name}: {e}")

    def list(self, label_selector=None):
        """List VirtualMachines."""
        result = self.custom_api.list_cluster_custom_object(
            **self.common_parameters,
            label_selector=label_selector
        )
        return result.get("items", [])

    def cleanup(self):
        try:
            scs = self.list(
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )

            for sc in scs:
                try:
                    sc_name = sc['metadata']['name']
                    logging(f'Deleting storage class: {sc_name}')
                    self.delete(sc_name)
                except Exception as e:
                    logging(f'Error deleting storage class: {sc_name}: {e}', 'WARNING')
        except Exception as e:
            logging(f'Error during storage class cleanup: {e}', 'WARNING')
