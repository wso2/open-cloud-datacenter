from collections.abc import Mapping

from .base import BaseManager, merge_dict


class HostManager(BaseManager):
    PATH_fmt = "v1/harvester/nodes/{uid}"
    METRIC_fmt = "v1/metrics.k8s.io.nodes/{uid}"

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name), raw=raw)

    def create(self, *args, **kwargs):
        raise NotImplementedError("Create new host is not allowed.")

    def update(self, name, data, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(uid=name)
        if isinstance(data, Mapping) and as_json:
            _, node = self.get(name)
            data = merge_dict(data, node)
        return self._update(path, data, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name), raw=raw)

    def get_metrics(self, name="", *, raw=False):
        return self._get(self.METRIC_fmt.format(uid=name), raw=raw)

    def maintenance_mode(self, name, enable=True, force=False):
        action = "enable" if enable else "disable"
        params = dict(action=f"{action}MaintenanceMode")
        payload = dict(force=str(force).lower())
        return self._create(self.PATH_fmt.format(uid=name), params=params, json=payload)

    def cpu_manager(self, name, enable=True):
        action = "enable" if enable else "disable"
        params = dict(action=f"{action}CPUManager")
        return self._create(self.PATH_fmt.format(uid=name), params=params)
