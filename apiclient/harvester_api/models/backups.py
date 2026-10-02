from copy import deepcopy


class _BaseBackup:
    def __init__(self, new_vm, vm_name=None, namespace=None, delete_volumes=None):
        self.new_vm = new_vm
        self.vm_name = vm_name
        self.namespace = namespace
        self.delete_volumes = delete_volumes

    def __repr__(self):
        return (f"{__class__.__name__}({self.new_vm},"
                f" {self.vm_name}, {self.namespace}, {self.delete_volumes})")

    def to_dict(self, name, namespace, existing_vm):
        data = {
            "type": "harvesterhci.io.virtualmachinerestore",
            "metadata": {
                "generateName": f"restore-{name}-",
                "name": "",
                "namespace": self.namespace or namespace
            },
            "spec": {
                "target": {
                    "apiGroup": "kubevirt.io",
                    "kind": "VirtualMachine",
                    "name": self.vm_name
                },
                "virtualMachineBackupName": name,
                "virtualMachineBackupNamespace": namespace
            }
        }
        spec = data['spec']
        if self.new_vm:
            spec['newVM'] = self.new_vm
        else:
            spec['deletionPolicy'] = "delete" if self.delete_volumes else "retain"
            spec['target']['name'] = existing_vm

        return deepcopy(data)


class RestoreSpec(_BaseBackup):
    @classmethod
    def for_new(cls, vm_name, namespace=None):
        return cls(True, vm_name, namespace)

    @classmethod
    def for_existing(cls, delete_volumes=True):
        return cls(False, delete_volumes=delete_volumes)


class SnapshotRestoreSpec(_BaseBackup):
    @classmethod
    def for_new(cls, vm_name):
        return cls(True, vm_name)

    @classmethod
    def for_existing(cls):
        return cls(False, delete_volumes=False)
