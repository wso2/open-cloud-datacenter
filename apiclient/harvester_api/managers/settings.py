from collections.abc import Mapping

from harvester_api.models.settings import BaseSettingSpec, BackupTargetSpec, \
    StorageNetworkSpec, KubeconfigDefaultTokenTTLSpec
from .base import BaseManager, merge_dict


class SettingManager(BaseManager):
    # get, update
    vlan = "apis/network.{API_VERSION}/clusternetworks/vlan"
    # api-ui-version, backup-target, cluster-registration-url
    PATH_fmt = "apis/{{API_VERSION}}/settings/{name}"
    # "v1/harvesterhci.io.settings/{name}"
    Spec = BaseSettingSpec
    BackupTargetSpec = BackupTargetSpec
    StorageNetworkSpec = StorageNetworkSpec
    KubeconfigDefaultTokenTTLSpec = KubeconfigDefaultTokenTTLSpec

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(name=name))

    def update(self, name, spec, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(name=name)
        _, node = self.get(name)
        if isinstance(spec, BaseSettingSpec):
            spec = spec.to_dict(node)
        if isinstance(spec, Mapping) and as_json:
            spec = merge_dict(spec, node)
        return self._update(path, spec, raw=raw, as_json=as_json, **kwargs)

    def backup_target_test_connection(self, *, raw=False):
        path = "/v1/harvester/backuptarget/healthz"
        return self._get(path, raw=raw)
