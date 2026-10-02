
"""
Volume Rest Implementation - drives the shared Harvester API client
(apiclient/harvester_api) via api.volumes / api.vol_snapshots.
"""
import time
from datetime import datetime, timedelta

from constant import (
    DEFAULT_NAMESPACE, DEFAULT_STORAGE_CLASS,
    VOLUME_STATE_BOUND,
    ACCESS_MODE_RWX,
    LABEL_TEST, LABEL_TEST_VALUE,
    DEFAULT_TIMEOUT_SHORT,
)
from utility.utility import get_harvester_api_client
from utility.utility import logging
from volume.base import Base


class Rest(Base):
    """Volume Rest implementation - makes actual API calls"""

    def __init__(self):
        pass

    def create(self, volume_name, size, numberOfReplicas, frontend, **kwargs):
        """Create a volume (PVC) through the Harvester REST API"""
        namespace = kwargs.get('namespace', DEFAULT_NAMESPACE)
        storage_class = kwargs.get('storage_class', DEFAULT_STORAGE_CLASS)
        access_mode = kwargs.get('access_mode', ACCESS_MODE_RWX)

        annotations = {}
        if numberOfReplicas:
            annotations['longhorn.io/number-of-replicas'] = str(numberOfReplicas)

        api = get_harvester_api_client()
        # api.volumes.Spec is version-aware (VolumeSpec / VolumeSpec180)
        spec = api.volumes.Spec(size, storage_cls=storage_class, annotations=annotations)
        # RWX / Block (Harvester's shareable VM volume; also the apiclient default)
        spec.access_modes = [access_mode]
        spec.volume_mode = "Block"

        body = spec.to_dict(volume_name, namespace)
        body['metadata'].setdefault('labels', {})[LABEL_TEST] = LABEL_TEST_VALUE

        code, data = api.volumes.create(volume_name, body, namespace=namespace)
        assert code in (200, 201), f"Failed to create volume: {code}, {data}"
        self.wait_for_attached(volume_name, DEFAULT_TIMEOUT_SHORT)
        return data

    def try_create(self, volume_name, size, numberOfReplicas=3,
                   frontend="blockdev", **kwargs):
        """Negative-test helper. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "try_create is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def exists(self, volume_name):
        """Existence check. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "exists is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def create_from_image(self, volume_name, image_name, size=None):
        """Create volume from image. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "create_from_image is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def try_expand(self, volume_name, new_size):
        """Negative-test helper. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "try_expand is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def get_image_id(self, volume_name):
        """Read imageId annotation. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "get_image_id is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def export_to_image(self, volume_name, image_name,
                        target_storage_class=DEFAULT_STORAGE_CLASS):
        """Export volume to image. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "export_to_image is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def create_concurrently(self, volume_names, size="10Gi"):
        """Concurrent create. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "create_concurrently is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def delete_concurrently(self, volume_names):
        """Concurrent delete. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "delete_concurrently is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def try_get(self, volume_name):
        """Negative-test helper. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "try_get is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def try_delete(self, volume_name):
        """Negative-test helper. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "try_delete is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def delete(self, volume_name, wait=True):
        """Delete a volume"""
        api = get_harvester_api_client()
        code, data = api.volumes.delete(volume_name)
        assert code in (200, 204), f"Failed to delete volume: {code}, {data}"
        if wait:
            self.wait_for_deleted(volume_name, DEFAULT_TIMEOUT_SHORT)

    def attach(self, volume_name, node_name):
        """PVCs are attached automatically when consumed by a VM/pod"""
        logging("PVCs are attached automatically when used by VMs/pods")

    def detach(self, volume_name):
        """PVCs are detached automatically when the consumer is removed"""
        logging("PVCs are detached automatically when VMs/pods are removed")

    def wait_for_running(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for volume to be Bound"""
        return self.wait_for_attached(volume_name, timeout)

    def wait_for_attached(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for the PVC to reach the Bound phase"""
        api = get_harvester_api_client()
        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.volumes.get(volume_name)
            phase = data.get('status', {}).get('phase') if code == 200 else None
            if phase == VOLUME_STATE_BOUND:
                return True
            logging(f"Waiting for volume {volume_name} to be bound (phase: {phase})...")
            time.sleep(3)
        raise AssertionError(f"Volume {volume_name} did not bind within {timeout}s")

    def wait_for_deleted(self, volume_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for volume to be deleted"""
        api = get_harvester_api_client()
        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.volumes.get(volume_name)
            if code == 404:
                return True
            time.sleep(3)
        raise AssertionError(f"Volume {volume_name} was not deleted within {timeout}s")

    def get_status(self, volume_name):
        """Get volume status (same shape as the CRD implementation)"""
        api = get_harvester_api_client()
        code, data = api.volumes.get(volume_name)
        assert code == 200, f"Failed to get volume status: {code}, {data}"
        status = data.get('status', {})
        return {
            'phase': status.get('phase', 'Unknown'),
            'capacity': status.get('capacity', {}),
            'access_modes': data.get('spec', {}).get('accessModes', []),
            'conditions': status.get('conditions', []) or []
        }

    def list(self):
        """List all volumes (same shape as the CRD implementation)"""
        api = get_harvester_api_client()
        code, data = api.volumes.get("")
        assert code == 200, f"Failed to list volumes: {code}, {data}"
        volumes = []
        for item in data.get('data', []):
            volumes.append({
                'metadata': {
                    'name': item['metadata']['name'],
                    'namespace': item['metadata']['namespace'],
                },
                'status': item.get('status', {}),
            })
        return volumes

    def expand(self, volume_name, new_size):
        """Expand volume size by updating the PVC spec"""
        api = get_harvester_api_client()
        code, data = api.volumes.get(volume_name)
        assert code == 200, f"Failed to get volume for expand: {code}, {data}"

        spec = api.volumes.Spec.from_dict(data)
        spec.size = new_size
        code, data = api.volumes.update(volume_name, spec)
        assert code in (200, 201), f"Failed to expand volume: {code}, {data}"

    def create_snapshot(self, volume_name, snapshot_name, snapshot_class=None):
        """Create a volume snapshot via the ?action=snapshot endpoint.

        The snapshot class is selected by Harvester for this action API, so the
        snapshot_class argument is accepted only for signature parity with CRD.
        """
        api = get_harvester_api_client()
        code, data = api.volumes.snapshot(volume_name, snapshot_name)
        assert code in (200, 201, 204), f"Failed to create snapshot: {code}, {data}"

    def get_snapshot_status(self, snapshot_name):
        """Get the status block of a VolumeSnapshot"""
        api = get_harvester_api_client()
        code, data = api.vol_snapshots.get(snapshot_name)
        assert code == 200, f"Failed to get snapshot status: {code}, {data}"
        return data.get('status', {})

    def wait_for_snapshot_ready(self, snapshot_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for a VolumeSnapshot to become readyToUse"""
        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            status = self.get_snapshot_status(snapshot_name)
            if status.get('readyToUse'):
                logging(f"VolumeSnapshot {snapshot_name} is ready")
                return True
            logging(f"Waiting for VolumeSnapshot {snapshot_name} to be ready...")
            time.sleep(3)
        raise AssertionError(f"VolumeSnapshot {snapshot_name} was not ready within {timeout}s")

    def delete_snapshot(self, volume_name, snapshot_name):
        """Delete a volume snapshot"""
        api = get_harvester_api_client()
        code, data = api.vol_snapshots.delete(snapshot_name)
        assert code in (200, 204), f"Failed to delete snapshot: {code}, {data}"

    def restore_from_snapshot(self, volume_name, snapshot_name, new_volume_name):
        """Restore a new volume from a snapshot via the ?action=restore endpoint.

        The action is asynchronous and returns 204 No Content; success is
        confirmed by the caller waiting for the restored PVC to reach Bound.
        The source snapshot must outlive that provisioning, so callers must not
        delete it until the restored volume is active.
        """
        api = get_harvester_api_client()
        code, data = api.vol_snapshots.restore(snapshot_name, new_volume_name)
        assert code in (200, 201, 204), f"Failed to restore from snapshot: {code}, {data}"

    def cleanup(self):
        """Clean up all test volumes labelled by the framework"""
        api = get_harvester_api_client()
        code, data = api.volumes.get("")
        if code != 200:
            logging(f"Skipping volume cleanup, list failed: {code}, {data}", "WARNING")
            return
        for item in data.get('data', []):
            labels = item['metadata'].get('labels', {}) or {}
            if labels.get(LABEL_TEST) == LABEL_TEST_VALUE:
                name = item['metadata']['name']
                try:
                    self.delete(name, wait=True)
                except Exception as e:
                    logging(f"Error deleting volume {name}: {e}", "WARNING")
