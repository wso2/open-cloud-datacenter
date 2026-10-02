from harvester_api.models.backups import RestoreSpec, SnapshotRestoreSpec
from .base import DEFAULT_NAMESPACE, BaseManager


class BackupManager(BaseManager):
    BACKUP_fmt = "v1/harvester/harvesterhci.io.virtualmachinebackups/{ns}{uid}"
    RESTORE_fmt = "v1/harvester/harvesterhci.io.virtualmachinerestores/{ns}"

    RestoreSpec = RestoreSpec

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        path = self.BACKUP_fmt.format(uid=f"/{name}", ns=namespace)
        resp = self._get(path, raw=raw, **kwargs)
        try:
            code, data = resp[0], resp[1]
            if name and "backup" != data['spec']['type']:
                return 404, dict(type='error', status=404, message=f'Backup {name!r} not found')

            data['data'] = [d for d in data['data'] if "backup" == d.get('spec', {}).get('type')]
            return code, data
        except TypeError:
            # raw=True
            return resp
        except KeyError:
            # !data.spec || !data.data
            return code, data

    def create(self, *args, **kwargs):
        # Delegate to vm.backups
        return self.api.vms.backup(*args, **kwargs)

    def update(
        self, name, backup_spec, namespace=DEFAULT_NAMESPACE, *,
        raw=False, as_json=True, **kwargs
    ):
        path = self.BACKUP_fmt.format(uid=f"/{name}", ns=namespace)
        return self._update(path, backup_spec, raw=raw, as_json=as_json, **kwargs)

    def restore(self, name, restore_spec, namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        code, data = self.get(name, namespace)
        try:
            old_vm = data['spec']['source']['name']
            spec = restore_spec.to_dict(name, namespace, old_vm)
            path = self.RESTORE_fmt.format(ns=restore_spec.namespace or namespace)
            return self._create(path, json=spec, raw=raw, **kwargs)
        except KeyError:
            return code, data

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        path = self.BACKUP_fmt.format(uid=f"/{name}", ns=namespace)
        return self._delete(path, raw=raw, **kwargs)


class VirtualMachineSnapshotManager(BackupManager):
    RestoreSpec = SnapshotRestoreSpec

    def create_data(self, vm_uid, vm_name, snapshot_name, namespace):
        return {
            "type": "harvesterhci.io.virtualmachinebackup",
            "spec": {
                "type": "snapshot",
                "source": {
                    "apiGroup": "kubevirt.io",
                    "kind": "VirtualMachine",
                    "name": vm_name
                }
            },
            "metadata": {
                "name": snapshot_name,
                "namespace": namespace,
                "ownerReferences": [{
                    "apiVersion": "kubevirt.io/v1",
                    "kind": "VirtualMachine",
                    "name": vm_name,
                    "uid": vm_uid
                }]
            }
        }

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        resp = super().get(name, namespace, raw=True, **kwargs)
        if raw:
            return resp
        code, data = resp.status_code, resp.json()
        try:
            if name and "snapshot" != data['spec']['type']:
                return 404, dict(type='error', status=404, message=f'Snapshot {name!r} not found')

            data['data'] = [d for d in data['data'] if "snapshot" == d.get('spec', {}).get('type')]
            return code, data
        except KeyError:
            # !data.spec || !data.data
            return code, data

    def create(self, vm_name, snapshot_name, namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        _, data = self.api.vms.get(vm_name, namespace)
        vm_uid = data.get('metadata', {}).get('uid', '')

        path = self.BACKUP_fmt.format(uid="", ns=namespace)
        spec = self.create_data(vm_uid, vm_name, snapshot_name, namespace)
        return self._create(path, json=spec, raw=raw, **kwargs)
