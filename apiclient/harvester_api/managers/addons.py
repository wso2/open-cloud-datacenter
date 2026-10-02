from collections.abc import Mapping

from harvester_api.models.addons import BaseAddonSpec, MonitoringAddonSpec
from .base import BaseManager, merge_dict


class AddonManager(BaseManager):
    PATH_fmt = "v1/harvester/harvesterhci.io.addons/{uid}"
    Spec = BaseAddonSpec
    MonitoringAddonSpec = MonitoringAddonSpec

    def get(self, uid="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=uid))

    def update(self, uid, spec, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(uid=uid)
        _, data = self.get(uid)
        if isinstance(spec, self.Spec):
            spec = spec.to_dict(data)
        elif isinstance(spec, Mapping) and as_json:
            spec = merge_dict(spec, data)
        return self._update(path, spec, raw=raw, as_json=as_json, **kwargs)

    def enable(self, uid, enable=True):
        path = self.PATH_fmt.format(uid=uid)
        code, data = self.get(uid)
        if 200 == code:
            data['spec']['enabled'] = enable
            code, data = self._update(path, data, as_json=True)
        return code, data

    def disable(self, uid):
        return self.enable(uid, False)
