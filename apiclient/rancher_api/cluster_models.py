from copy import deepcopy
from enum import Flag, auto
from operator import or_, xor
from functools import reduce


class Quota:
    def __init__(self, data=None):
        self._data = data or dict()

    @property
    def cpu_limit(self):
        return self._data.get("limitsCpu")

    @cpu_limit.setter
    def cpu_limit(self, val):
        if not isinstance(val, str):
            val = f"{val}m"
        self._data['limitsCpu'] = val

    @property
    def mem_limit(self):
        return self._data.get("limitsMemory")

    @mem_limit.setter
    def mem_limit(self, val):
        if not isinstance(val, str):
            val = f"{val}Mi"
        self._data['limitsMemory'] = val

    @property
    def cpu_request(self):
        return self._data.get("requestsCpu")

    @cpu_request.setter
    def cpu_request(self, val):
        if not isinstance(val, str):
            val = f"{val}m"
        self._data['requestsCpu'] = val

    @property
    def mem_request(self):
        return self._data.get("requestsMemory")

    @mem_request.setter
    def mem_request(self, val):
        if not isinstance(val, str):
            val = f"{val}Mi"
        self._data['requestsMemory'] = val


class ResourceQuota(Quota):
    @property
    def storage_request(self):
        return self._data.get("requestsStorage")

    @storage_request.setter
    def storage_request(self, val):
        if not isinstance(val, str):
            val = f"{val}Mi"
        self._data['requestsStorage'] = val

    @property
    def config_maps(self):
        return self._data.get("configMaps")

    @config_maps.setter
    def config_maps(self, val):
        self._data['configMaps'] = str(val)

    @property
    def pvc(self):
        return self._data.get("persistentVolumeClaims")

    @pvc.setter
    def pvc(self, val):
        self._data['persistentVolumeClaims'] = str(val)

    @property
    def load_balancers(self):
        return self._data.get("servicesLoadBalancers")

    @load_balancers.setter
    def load_balancers(self, val):
        self._data['servicesLoadBalancers'] = str(val)

    @property
    def node_ports(self):
        return self._data.get("servicesNodePorts")

    @node_ports.setter
    def node_ports(self, val):
        self._data['servicesNodePorts'] = str(val)

    @property
    def pods(self):
        return self._data.get("pods")

    @pods.setter
    def pods(self, val):
        self._data['pods'] = str(val)

    @property
    def secrets(self):
        return self._data.get("secrets")

    @secrets.setter
    def secrets(self, val):
        self._data['secrets'] = str(val)

    @property
    def services(self):
        return self._data.get("services")

    @services.setter
    def services(self, val):
        self._data['services'] = str(val)


class AccessModes(Flag):
    ReadWriteOnce = auto()
    ReadWriteMany = auto()
    ReadOnlyMany = auto()

    def __iter__(self):
        # Only for Python version < 3.11
        # ref: https://github.com/python/cpython/blob/e643412ef443b7a6847833f1e140b567622ac383/Lib/enum.py#L1361 # noqa
        num = self._value_
        while num:
            val = num & (~num + 1)
            yield self._value2member_map_.get(val)
            num ^= val


class PersistentVolumeClaimSpec:
    _data = None

    def __init__(self, size, storage_cls="", description="", *, labels=None, annotations=None):
        self.access_modes = AccessModes.ReadWriteOnce

        self.size = size
        self.storage_cls = storage_cls
        self.description = description
        self.labels = labels
        self.annotations = annotations

    @property
    def mode_rwo(self):
        return bool(self.access_modes & AccessModes.ReadWriteOnce)

    @mode_rwo.setter
    def mode_rwo(self, enable):
        op = or_ if enable else xor
        self.access_modes = op(self.access_modes, AccessModes.ReadWriteOnce)

    @property
    def mode_rwx(self):
        return bool(self.access_modes & AccessModes.ReadWriteMany)

    @mode_rwx.setter
    def mode_rwx(self, enable):
        op = or_ if enable else xor
        self.access_modes = op(self.access_modes, AccessModes.ReadWriteMany)

    @property
    def mode_rom(self):
        return bool(self.access_modes & AccessModes.ReadOnlyMany)

    @mode_rom.setter
    def mode_rom(self, enable):
        op = or_ if enable else xor
        self.access_modes = op(self.access_modes, AccessModes.ReadOnlyMany)

    def to_dict(self, name, namespace, volume=None):
        labels = self.labels or dict()
        annotations = self.annotations or dict()
        size = f"{self.size}Gi" if isinstance(self.size, (int, float)) else self.size
        modes = [m.name for m in self.access_modes]
        data = {
            "type": "persistentvolumeclaim",
            "metadata": {
                "namespace": namespace,
                "name": name,
                "annotations": annotations,
                "labels": labels
            },
            "spec": {
                "accessModes": modes,
                "volumeName": volume or (self._data and self._data['spec'].get('volumeName')),
                "resources": {
                    "requests": dict(storage=size)
                }
            },
            "accessModes": modes
        }

        if self.storage_cls or volume:
            data['spec']['storageClassName'] = "" if volume else self.storage_cls

        if self.description:
            data['metadata']['annotations']['field.cattle.io/description'] = self.description

        if self._data:
            self._data['metadata'].update(data['metadata'])
            self._data['spec'].update(data['spec'])
            return deepcopy(self._data)

        return deepcopy(data)

    @classmethod
    def from_dict(cls, data):
        spec, metadata = data['spec'], data['metadata']
        annotations = metadata.get('annotations', {})
        size = spec['resources']['requests']['storage']
        storage_cls = spec.get('storageClassName')

        obj = cls(size, storage_cls, annotations.get("field.cattle.io/description"))
        obj.annotations = annotations
        obj.labels = metadata.get('labels', {})
        obj.access_modes = reduce(or_, (getattr(AccessModes, m) for m in spec['accessModes']))
        obj._data = data

        return obj


class ProjectSpec:
    _data = None

    def __init__(self, *, labels=None, annotations=None):
        self.labels = labels
        self.annotations = annotations
        self.vm_quota = Quota()
        self.project_quota = ResourceQuota()
        self.namespace_quota = ResourceQuota()

    def to_dict(self, name, cluster, creator=None):
        data = {
            "type": "project",
            "name": name,
            "clusterId": cluster,
            "annotations": self.annotations or dict(),
            "labels": self.labels or dict(),
            "resourceQuota": {"limit": self.project_quota._data},
            "namespaceDefaultResourceQuota": {"limit": self.namespace_quota._data},
            "containerDefaultResourceLimit": self.vm_quota._data
        }
        if creator:
            data["creatorId"] = creator

        return deepcopy(data)

    @classmethod
    def from_dict(cls, data):
        data = deepcopy(data)
        labels = data.get('labels', {})
        anno = data.get('annotations', {})
        obj = cls(labels=labels, annotations=anno)
        obj.vm_quota._data = data.get('containerDefaultResourceLimit', {})
        obj.project_quota._data = data.get('resourceQuota', {}).get('limit', {})
        obj.namespace_quota._data = data.get("namespaceDefaultResourceQuota", {}).get("limit", {})

        obj._data = data
        return obj
