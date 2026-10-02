from .base import BaseManager

DEFAULT_LONGHORN_NAMESPACE = "longhorn-system"


class LonghornReplicaManager(BaseManager):
    API_VERSION = "longhorn.io/v1beta2"
    PATH_fmt = "apis/{API_VERSION}/namespaces/{namespace}/replicas/{name}"

    API_PATH_fmt = "v1/harvester/longhorn.io.replicas/{namespace}{name}"

    def get(self, name="", namespace=DEFAULT_LONGHORN_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(API_VERSION=self.API_VERSION, name=name, namespace=namespace)
        return self._get(path, raw=raw, **kwargs)

    def delete(self, name="", namespace=DEFAULT_LONGHORN_NAMESPACE, *, raw=False):
        path = self.API_PATH_fmt.format(name=f"/{name}", namespace=namespace)
        return self._delete(path, raw=raw)


class LonghornVolumeManager(BaseManager):
    API_VERSION = "longhorn.io/v1beta2"
    PATH_fmt = "apis/{API_VERSION}/namespaces/{namespace}/volumes/{name}"

    API_PATH_fmt = "v1/harvester/longhorn.io.volumes/{namespace}{name}"

    def get(self, name="", namespace=DEFAULT_LONGHORN_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(API_VERSION=self.API_VERSION, name=name, namespace=namespace)
        return self._get(path, raw=raw, **kwargs)


class LonghornBackupVolumeManager(BaseManager):
    API_VERSION = "longhorn.io/v1beta2"
    PATH_fmt = "apis/{API_VERSION}/namespaces/{namespace}/backupvolumes/{name}"
    API_PATH_fmt = "v1/harvester/longhorn.io.backupvolumes/{namespace}{name}"

    def get(self, name="", namespace=DEFAULT_LONGHORN_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(API_VERSION=self.API_VERSION, name=name, namespace=namespace)
        return self._get(path, raw=raw, **kwargs)

    def delete(self, name="", namespace=DEFAULT_LONGHORN_NAMESPACE, *, raw=False):
        path = self.API_PATH_fmt.format(name=f"/{name}", namespace=namespace)
        return self._delete(path, raw=raw)
