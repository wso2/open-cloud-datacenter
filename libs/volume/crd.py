# flake8: noqa: F405
"""
Volume CRD Implementation - uses Kubernetes PersistentVolumeClaim for volume operations
"""
import math
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from kubernetes import client
from kubernetes.client.rest import ApiException
from crd import get_cr, create_cr
from constant import (
    DEFAULT_NAMESPACE, DEFAULT_STORAGE_CLASS,
    DEFAULT_VOLUME_SNAPSHOT_CLASS,
    VOLUME_STATE_BOUND,
    ACCESS_MODE_RWX,
    LABEL_TEST, LABEL_TEST_VALUE,
    DEFAULT_TIMEOUT_SHORT,
    HARVESTER_API_GROUP, HARVESTER_API_VERSION,
    VIRTUALMACHINEIMAGE_PLURAL,
)
from utility.utility import logging, get_retry_count_and_interval
from volume.base import Base


class CRD(Base):
    """
    Volume CRD implementation - uses Kubernetes PersistentVolumeClaim (PVC) resources
    """

    def __init__(self):
        self.core_api = client.CoreV1Api()
        self.retry_count, self.retry_interval = get_retry_count_and_interval()

    def create(self, volume_name, size, numberOfReplicas, frontend, **kwargs):
        """
        Create a PersistentVolumeClaim

        Note: Harvester uses Longhorn for storage, which is provisioned via StorageClass
        """
        namespace = kwargs.get('namespace', DEFAULT_NAMESPACE)
        storage_class = kwargs.get('storage_class', DEFAULT_STORAGE_CLASS)
        access_mode = kwargs.get('access_mode', ACCESS_MODE_RWX)

        # Build PVC manifest
        body = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": {
                "name": volume_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                },
                "annotations": {
                    "volume.kubernetes.io/storage-provisioner": "driver.longhorn.io"    # Noqa
                }
            },
            "spec": {
                "accessModes": [access_mode],
                "volumeMode": "Block",
                "storageClassName": storage_class,
                "resources": {
                    "requests": {
                        "storage": size
                    }
                }
            }
        }

        # Add Longhorn-specific parameters
        if numberOfReplicas:
            if 'annotations' not in body['metadata']:
                body['metadata']['annotations'] = {}
            body['metadata']['annotations'][
                'longhorn.io/number-of-replicas'
            ] = str(numberOfReplicas)

        try:
            logging(f"Creating PersistentVolumeClaim {namespace}/{volume_name}")
            pvc = self.core_api.create_namespaced_persistent_volume_claim(
                namespace=namespace,
                body=body
            )

            # Wait for PVC to be created
            self.wait_for_volume_created(volume_name, namespace)

            return {
                'metadata': {
                    'name': pvc.metadata.name,
                    'namespace': pvc.metadata.namespace
                },
                'status': {
                    'phase': pvc.status.phase if pvc.status else 'Pending'
                }
            }

        except ApiException as e:
            logging(f"Failed to create PVC {volume_name}: {e}")
            raise Exception(f"Failed to create volume: {e.status}, {e.reason}")

    def create_from_image(self, volume_name, image_name, size=None,
                          namespace=DEFAULT_NAMESPACE):
        """Create a PVC backed by a VirtualMachineImage.

        Harvester provisions a volume from an image with an ordinary PVC that
        (a) uses the image's own StorageClass (status.storageClassName) and
        (b) carries the harvesterhci.io/imageId annotation pointing at the
        image. No REST action endpoint is involved - this is pure CRD/PVC.

        size defaults to the image's virtual size rounded up to whole GiB.
        """
        image = get_cr(
            group=HARVESTER_API_GROUP,
            version=HARVESTER_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEIMAGE_PLURAL,
            name=image_name,
        )
        image_status = image.get("status", {})
        storage_class = image_status.get("storageClassName")
        assert storage_class, (
            f"Image {namespace}/{image_name} has no status.storageClassName "
            f"yet; is it fully imported?")

        if size is None:
            virtual_size = image_status.get("virtualSize", 0)
            size = f"{max(1, math.ceil(virtual_size / 1024 ** 3))}Gi"

        image_id = f"{namespace}/{image_name}"
        body = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": {
                "name": volume_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                },
                "annotations": {
                    "harvesterhci.io/imageId": image_id
                }
            },
            "spec": {
                "accessModes": [ACCESS_MODE_RWX],
                "volumeMode": "Block",
                "storageClassName": storage_class,
                "resources": {
                    "requests": {
                        "storage": size
                    }
                }
            }
        }

        try:
            logging(f"Creating PVC {namespace}/{volume_name} from image "
                    f"{image_id} (sc={storage_class}, size={size})")
            self.core_api.create_namespaced_persistent_volume_claim(
                namespace=namespace,
                body=body
            )
            self.wait_for_volume_created(volume_name, namespace)
            return {
                "metadata": {"name": volume_name, "namespace": namespace},
                "image_id": image_id,
                "storage_class": storage_class,
            }
        except ApiException as e:
            logging(f"Failed to create PVC from image {volume_name}: {e}")
            raise Exception(
                f"Failed to create volume from image: {e.status}, {e.reason}")

    def get_image_id(self, volume_name, namespace=DEFAULT_NAMESPACE):
        """Return the harvesterhci.io/imageId annotation of a PVC (or '')."""
        pvc = self.get(volume_name, namespace)
        annotations = pvc.get("metadata", {}).get("annotations", {}) or {}
        return annotations.get("harvesterhci.io/imageId", "")

    def export_to_image(self, volume_name, image_name,
                        target_storage_class=DEFAULT_STORAGE_CLASS,
                        namespace=DEFAULT_NAMESPACE):
        """Start exporting a volume (PVC) to a VirtualMachineImage.

        This is the CRD form of the REST `?action=export`: it creates a
        VirtualMachineImage with sourceType=export-from-volume pointing at the
        source PVC (pvcName/pvcNamespace). The export then runs asynchronously;
        while it is in progress the source PVC cannot be deleted.
        """
        body = {
            "apiVersion": f"{HARVESTER_API_GROUP}/{HARVESTER_API_VERSION}",
            "kind": "VirtualMachineImage",
            "metadata": {
                "name": image_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE,
                    "harvesterhci.io/imageDisplayName": image_name
                },
                "annotations": {
                    "harvesterhci.io/storageClassName": target_storage_class
                }
            },
            "spec": {
                "displayName": image_name,
                "sourceType": "export-from-volume",
                "pvcName": volume_name,
                "pvcNamespace": namespace,
                "targetStorageClassName": target_storage_class
            }
        }
        logging(f"Exporting volume {namespace}/{volume_name} to image "
                f"{image_name} (sc={target_storage_class})")
        return create_cr(
            group=HARVESTER_API_GROUP,
            version=HARVESTER_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEIMAGE_PLURAL,
            body=body
        )

    def create_concurrently(self, volume_names, size="10Gi", numberOfReplicas=3,
                            timeout=DEFAULT_TIMEOUT_SHORT):
        """Create several volumes in parallel and wait for each to bind.

        Stresses the API server / Longhorn concurrent provisioning path.
        Returns a list of {name, success, error} dicts (one per volume).
        """
        def _worker(name):
            try:
                self.create(name, size, numberOfReplicas, "blockdev")
                self.wait_for_attached(name, timeout)   # reach Bound
                return {"name": name, "success": True, "error": ""}
            except Exception as e:
                return {"name": name, "success": False, "error": str(e)}

        results = []
        with ThreadPoolExecutor(max_workers=len(volume_names)) as executor:
            futures = [executor.submit(_worker, n) for n in volume_names]
            for future in as_completed(futures):
                results.append(future.result())
        return results

    def delete_concurrently(self, volume_names, timeout=DEFAULT_TIMEOUT_SHORT):
        """Delete several volumes in parallel and wait for each to be gone.

        Returns a list of {name, success, error} dicts (one per volume).
        """
        def _worker(name):
            try:
                self.delete(name, wait=True)
                return {"name": name, "success": True, "error": ""}
            except Exception as e:
                return {"name": name, "success": False, "error": str(e)}

        results = []
        with ThreadPoolExecutor(max_workers=len(volume_names)) as executor:
            futures = [executor.submit(_worker, n) for n in volume_names]
            for future in as_completed(futures):
                results.append(future.result())
        return results

    def try_create(self, volume_name, size, numberOfReplicas=3,
                   frontend="blockdev", **kwargs):
        """Attempt to create a PVC for negative testing.

        Unlike create(), this never raises when the API server rejects the
        request; it returns a result dict so callers can assert on the failure
        code and message.

        Returns: {"success": bool, "code": int, "message": str}
        """
        namespace = kwargs.get('namespace', DEFAULT_NAMESPACE)
        storage_class = kwargs.get('storage_class', DEFAULT_STORAGE_CLASS)
        access_mode = kwargs.get('access_mode', ACCESS_MODE_RWX)

        body = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": {
                "name": volume_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                }
            },
            "spec": {
                "accessModes": [access_mode],
                "volumeMode": "Block",
                "storageClassName": storage_class,
                "resources": {
                    "requests": {
                        "storage": size
                    }
                }
            }
        }

        try:
            self.core_api.create_namespaced_persistent_volume_claim(
                namespace=namespace,
                body=body
            )
            logging(f"PVC {namespace}/{volume_name} was unexpectedly created "
                    f"with size '{size}'", "WARNING")
            return {"success": True, "code": 201, "message": ""}
        except ApiException as e:
            # e.body carries the API server's validation message (JSON string);
            # fall back to reason when the body is empty.
            message = e.body or e.reason or ""
            logging(f"PVC {namespace}/{volume_name} rejected as expected "
                    f"(status={e.status}): {e.reason}")
            return {"success": False, "code": e.status, "message": message}

    def delete(self, volume_name, wait=True):
        """Delete a PersistentVolumeClaim"""
        namespace = DEFAULT_NAMESPACE

        try:
            logging(f"Deleting PersistentVolumeClaim {namespace}/{volume_name}")
            self.core_api.delete_namespaced_persistent_volume_claim(
                name=volume_name,
                namespace=namespace
            )

            if wait:
                self.wait_for_deleted(volume_name)

        except ApiException as e:
            if e.status != 404:  # Ignore not found errors
                logging(f"Error deleting PVC {volume_name}: {e}")
                raise

    def get(self, volume_name, namespace=DEFAULT_NAMESPACE):
        """Get a PersistentVolumeClaim"""
        try:
            pvc = self.core_api.read_namespaced_persistent_volume_claim(
                name=volume_name,
                namespace=namespace
            )

            return {
                'metadata': {
                    'name': pvc.metadata.name,
                    'namespace': pvc.metadata.namespace,
                    'labels': pvc.metadata.labels or {},
                    'annotations': pvc.metadata.annotations or {}
                },
                'spec': {
                    'accessModes': pvc.spec.access_modes or [],
                    'storageClassName': pvc.spec.storage_class_name,
                    'resources': {
                        'requests': {
                            'storage': (
                                pvc.spec.resources.requests.get('storage')
                                if pvc.spec.resources else None
                            )
                        }
                    }
                },
                'status': {
                    'phase': pvc.status.phase if pvc.status else 'Unknown',
                    'capacity': (
                        dict(pvc.status.capacity)
                        if pvc.status and pvc.status.capacity else {}
                    ),
                    'conditions': pvc.status.conditions if pvc.status.conditions else []
                }
            }
        except ApiException as e:
            logging(f"Failed to get PVC {volume_name}: {e}")
            raise

    def exists(self, volume_name, namespace=DEFAULT_NAMESPACE):
        """Return True if the PVC exists, False if it returns 404.

        Used by negative tests to confirm no resource is left behind after a
        rejected creation.
        """
        try:
            self.get(volume_name, namespace)
            return True
        except ApiException as e:
            if e.status == 404:
                return False
            raise

    def try_get(self, volume_name, namespace=DEFAULT_NAMESPACE):
        """Attempt to get a PVC for negative testing.

        Never raises; returns {"success": bool, "code": int, "message": str}.
        """
        try:
            self.get(volume_name, namespace)
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def try_delete(self, volume_name, namespace=DEFAULT_NAMESPACE):
        """Attempt to delete a PVC for negative testing.

        Unlike delete(), this does NOT swallow a 404; it returns the result so
        callers can assert that deleting a missing volume is rejected.
        Returns {"success": bool, "code": int, "message": str}.
        """
        try:
            self.core_api.delete_namespaced_persistent_volume_claim(
                name=volume_name,
                namespace=namespace
            )
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            logging(f"Delete of PVC {namespace}/{volume_name} returned "
                    f"status={e.status}: {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def list(self, namespace=DEFAULT_NAMESPACE, label_selector=None):
        """List PersistentVolumeClaims"""
        try:
            pvcs = self.core_api.list_namespaced_persistent_volume_claim(
                namespace=namespace,
                label_selector=label_selector
            )

            result = []
            for pvc in pvcs.items:
                result.append({
                    'metadata': {
                        'name': pvc.metadata.name,
                        'namespace': pvc.metadata.namespace
                    },
                    'status': {
                        'phase': pvc.status.phase if pvc.status else 'Unknown'
                    }
                })

            return result
        except ApiException as e:
            logging(f"Failed to list PVCs: {e}")
            raise

    def attach(self, volume_name, node_name):
        """
        Attach volume to node
        Note: PVCs are automatically attached when used by a pod
        This is a no-op for PVCs
        """
        logging("PVCs are attached automatically when used by pods")
        pass

    def detach(self, volume_name):
        """
        Detach volume
        Note: PVCs are automatically detached when pod is deleted
        This is a no-op for PVCs
        """
        logging("PVCs are detached automatically when pods are deleted")
        pass

    def wait_for_volume_created(self, volume_name, namespace=DEFAULT_NAMESPACE):
        """Wait for PVC to be created"""
        for i in range(self.retry_count):
            try:
                self.get(volume_name, namespace)
                logging(f"PVC {namespace}/{volume_name} created")
                return True
            except Exception:
                logging(f"Waiting for PVC to be created ({i})...")
                time.sleep(self.retry_interval)

        raise AssertionError(f"PVC {namespace}/{volume_name} not created")

    def wait_for_running(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for volume to be bound"""
        return self.wait_for_attached(volume_name, timeout)

    def wait_for_attached(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for PVC to be bound"""
        namespace = DEFAULT_NAMESPACE
        endtime = time.time() + timeout

        while time.time() < endtime:
            try:
                pvc = self.get(volume_name, namespace)
                phase = pvc.get('status', {}).get('phase')

                if phase == VOLUME_STATE_BOUND:
                    logging(f"PVC {namespace}/{volume_name} is bound")
                    return True

                logging(f"Waiting for PVC to be bound (current phase: {phase})...")

            except Exception as e:
                logging(f"Error checking PVC status: {e}")

            time.sleep(self.retry_interval)

        raise AssertionError(f"PVC {namespace}/{volume_name} did not bind within {timeout}s")

    def wait_for_deleted(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for PVC to be deleted"""
        namespace = DEFAULT_NAMESPACE
        endtime = time.time() + timeout

        while time.time() < endtime:
            try:
                self.get(volume_name, namespace)
                logging("Waiting for PVC to be deleted...")
            except ApiException as e:
                if e.status == 404:
                    logging(f"PVC {namespace}/{volume_name} deleted")
                    return True

            time.sleep(self.retry_interval)

        raise AssertionError(f"PVC {namespace}/{volume_name} was not deleted within {timeout}s")

    def get_status(self, volume_name):
        """Get volume status"""
        pvc = self.get(volume_name)
        status = pvc.get('status', {})

        return {
            'phase': status.get('phase', 'Unknown'),
            'capacity': status.get('capacity', {}),
            'access_modes': pvc.get('spec', {}).get('accessModes', []),
            'conditions': status.get('conditions', [])
        }

    def expand(self, volume_name, new_size):
        """
        Expand volume size
        This requires patching the PVC spec.resources.requests.storage
        """
        namespace = DEFAULT_NAMESPACE

        try:
            # Patch PVC to increase size
            body = {
                "spec": {
                    "resources": {
                        "requests": {
                            "storage": new_size
                        }
                    }
                }
            }

            self.core_api.patch_namespaced_persistent_volume_claim(
                name=volume_name,
                namespace=namespace,
                body=body
            )

            logging(f"PVC {namespace}/{volume_name} expanded to {new_size}")
        except ApiException as e:
            logging(f"Failed to expand PVC: {e}")
            raise

    def try_expand(self, volume_name, new_size, namespace=DEFAULT_NAMESPACE):
        """Attempt to resize a PVC for negative testing.

        Never raises on API rejection; returns {success, code, message} so a
        caller can assert that shrinking is forbidden. K8s rejects a shrink at
        the API server, so this fires regardless of REST/CRD path.
        """
        body = {
            "spec": {
                "resources": {
                    "requests": {
                        "storage": new_size
                    }
                }
            }
        }
        try:
            self.core_api.patch_namespaced_persistent_volume_claim(
                name=volume_name,
                namespace=namespace,
                body=body
            )
            logging(f"PVC {namespace}/{volume_name} resize to {new_size} "
                    f"was unexpectedly accepted", "WARNING")
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            logging(f"Resize of PVC {namespace}/{volume_name} to {new_size} "
                    f"rejected (status={e.status}): {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def create_snapshot(self, volume_name, snapshot_name,
                        snapshot_class=DEFAULT_VOLUME_SNAPSHOT_CLASS):
        """
        Create volume snapshot
        Note: This uses VolumeSnapshot CRD
        """
        namespace = DEFAULT_NAMESPACE

        body = {
            "apiVersion": "snapshot.storage.k8s.io/v1",
            "kind": "VolumeSnapshot",
            "metadata": {
                "name": snapshot_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                }
            },
            "spec": {
                "volumeSnapshotClassName": snapshot_class,
                "source": {
                    "persistentVolumeClaimName": volume_name
                }
            }
        }

        try:
            obj_api = client.CustomObjectsApi()
            obj_api.create_namespaced_custom_object(
                group="snapshot.storage.k8s.io",
                version="v1",
                namespace=namespace,
                plural="volumesnapshots",
                body=body
            )

            logging(f"VolumeSnapshot {namespace}/{snapshot_name} created")
        except ApiException as e:
            logging(f"Failed to create snapshot: {e}")
            raise

    def get_snapshot_status(self, snapshot_name, namespace=DEFAULT_NAMESPACE):
        """Get the status of a VolumeSnapshot"""
        obj_api = client.CustomObjectsApi()
        snapshot = obj_api.get_namespaced_custom_object(
            group="snapshot.storage.k8s.io",
            version="v1",
            namespace=namespace,
            plural="volumesnapshots",
            name=snapshot_name
        )
        return snapshot.get('status', {})

    def wait_for_snapshot_ready(self, snapshot_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for a VolumeSnapshot to become readyToUse"""
        namespace = DEFAULT_NAMESPACE
        endtime = time.time() + timeout

        while time.time() < endtime:
            try:
                status = self.get_snapshot_status(snapshot_name, namespace)
                if status.get('readyToUse'):
                    logging(f"VolumeSnapshot {namespace}/{snapshot_name} is ready")
                    return True
                logging(f"Waiting for VolumeSnapshot {snapshot_name} to be ready...")
            except ApiException as e:
                logging(f"Error checking snapshot status: {e}")

            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VolumeSnapshot {namespace}/{snapshot_name} was not ready within {timeout}s"
        )

    def delete_snapshot(self, volume_name, snapshot_name):
        """Delete volume snapshot"""
        namespace = DEFAULT_NAMESPACE

        try:
            obj_api = client.CustomObjectsApi()
            obj_api.delete_namespaced_custom_object(
                group="snapshot.storage.k8s.io",
                version="v1",
                namespace=namespace,
                plural="volumesnapshots",
                name=snapshot_name
            )

            logging(f"VolumeSnapshot {namespace}/{snapshot_name} deleted")
        except ApiException as e:
            if e.status != 404:
                logging(f"Failed to delete snapshot: {e}")
                raise

    def restore_from_snapshot(self, volume_name, snapshot_name, new_volume_name):
        """Restore volume from snapshot"""
        namespace = DEFAULT_NAMESPACE
        storage_class = DEFAULT_STORAGE_CLASS

        # Get original volume to get size
        original_pvc = self.get(volume_name, namespace)
        size = original_pvc['spec']['resources']['requests']['storage']

        # Create new PVC from snapshot
        body = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": {
                "name": new_volume_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                }
            },
            "spec": {
                "accessModes": [ACCESS_MODE_RWX],
                "volumeMode": "Block",
                "storageClassName": storage_class,
                "resources": {
                    "requests": {
                        "storage": size
                    }
                },
                "dataSource": {
                    "name": snapshot_name,
                    "kind": "VolumeSnapshot",
                    "apiGroup": "snapshot.storage.k8s.io"
                }
            }
        }

        try:
            self.core_api.create_namespaced_persistent_volume_claim(
                namespace=namespace,
                body=body
            )

            logging(f"PVC {namespace}/{new_volume_name} created from snapshot {snapshot_name}")
        except ApiException as e:
            logging(f"Failed to restore from snapshot: {e}")
            raise

    def cleanup(self):
        """Clean up all test volumes"""
        logging('Cleaning up test volumes')

        try:
            # List all PVCs with test label
            pvcs = self.list(
                namespace=DEFAULT_NAMESPACE,
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )

            for pvc in pvcs:
                pvc_name = pvc['metadata']['name']
                namespace = pvc['metadata']['namespace']

                try:
                    logging(f'Deleting test PVC: {namespace}/{pvc_name}')
                    # Wait to avoid issues when following cleanup like image and storageclass
                    self.delete(pvc_name, wait=True)
                except Exception as e:
                    logging(f'Error deleting PVC {pvc_name}: {e}', "WARNING")
        except Exception as e:
            logging(f'Error during volume cleanup: {e}', 'WARNING')
