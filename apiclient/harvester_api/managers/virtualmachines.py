from harvester_api.models.virtualmachines import VMSpec, VMSpec160, VMSpec180
from .base import DEFAULT_NAMESPACE, BaseManager


class VirtualMachineManager(BaseManager):
    API_VERSION = "kubevirt.io/v1"

    # operators: start, restart, stop, migrate, pause, unpause, softreboot
    PATH_fmt = "v1/harvester/kubevirt.io.virtualmachines/{ns}{uid}"
    # guestinfo, network
    VMI_fmt = "v1/harvester/kubevirt.io.virtualmachineinstances/{ns}/{uid}"
    # operators: guestosinfo, console(ws), vnc(ws)
    VMIOP_fmt = "apis/subresources.{VM_API}/namespaces/{ns}/virtualmachineinstances/{uid}/{op}"

    Spec = VMSpec

    def download_virtctl(self, *, raw=False, **kwargs):
        code, info = self._get(f"apis/subresources.{self.VM_API}/v1/version")
        version, platform = info['gitVersion'], info['platform']
        resp = self.api.session.get("https://github.com/kubevirt/kubevirt/releases/download/"
                                    f"{version}/virtctl-{version}-{platform}", **kwargs)
        if raw:
            return resp
        else:
            return resp.status_code, resp.content

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        return self._get(path, raw=raw, **kwargs)

    def get_status(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        path = self.VMI_fmt.format(uid=name, ns=namespace)
        return self._get(path, raw=raw, **kwargs)

    def create(self, name, vm_spec, namespace=DEFAULT_NAMESPACE, *, raw=False):
        if isinstance(vm_spec, self.Spec):
            vm_spec = self.Spec.to_dict(vm_spec, name, namespace)
            vm_spec['metadata'].pop('resourceVersion', None)  # remove for create new ones
        path = self.PATH_fmt.format(uid="", ns=namespace)
        return self._create(path, json=vm_spec, raw=raw)

    def update(self, name, vm_spec, namespace=DEFAULT_NAMESPACE, *,
               raw=False, as_json=True, **kwargs):
        if isinstance(vm_spec, self.Spec):
            vm_spec = self.Spec.to_dict(vm_spec, name, namespace)
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        return self._update(path, vm_spec, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        return self._delete(path, raw=raw, **kwargs)

    def clone(self, name, new_vm_name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="clone")
        return self._create(path, raw=raw, params=params, json=dict(targetVm=new_vm_name))

    def backup(self, name, backup_name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="backup")
        return self._create(path, raw=raw, params=params, json=dict(name=backup_name))

    def snapshot(self, *args, **kwargs):
        # delegate to vm_snapshot.create
        return self.api.vm_snapshots.create(*args, **kwargs)

    def start(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="start")
        return self._create(path, raw=raw, params=params)

    def restart(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="restart")
        return self._create(path, raw=raw, params=params)

    def stop(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="stop")
        return self._create(path, raw=raw, params=params)

    def migrate(self, name, target_node, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="migrate")
        return self._create(path, raw=raw, params=params, json=dict(nodeName=target_node))

    def abort_migrate(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="abortMigration")
        return self._create(path, raw=raw, params=params)

    def pause(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="pause")
        return self._create(path, raw=raw, params=params)

    def unpause(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="unpause")
        return self._create(path, raw=raw, params=params)

    def softreboot(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="softreboot")
        return self._create(path, raw=raw, params=params)

    def add_volume(self, name, disk_name, volume_name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="addVolume")
        json = dict(diskName=disk_name, volumeSourceName=volume_name)
        return self._create(path, params=params, json=json, raw=raw)

    def remove_volume(self, name, disk_name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        json = dict(diskName=disk_name)
        params = dict(action="removeVolume")
        return self._create(path, params=params, json=json, raw=raw)

    def create_template(
        self, name, template_name, keep_data=False, description="", namespace=DEFAULT_NAMESPACE,
        *, raw=False
    ):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        json = dict(description=description, name=template_name, withData=keep_data)
        params = dict(action="createTemplate")
        return self._create(path, params=params, json=json, raw=raw)

    def get_migratables(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="findMigratableNodes")
        return self._create(path, params=params, raw=raw)


class VMManager160(VirtualMachineManager):
    # ref: https://github.com/harvester/tests/issues/1695
    support_to = "v1.6.0"
    Spec = VMSpec160


class VMManager180(VMManager160):
    # ref: https://github.com/harvester/harvester/pull/9392
    support_to = "v1.8.0rc1"
    Spec = VMSpec180
