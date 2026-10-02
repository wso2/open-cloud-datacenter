from .base import BaseManager

DEFAULT_HARVESTER_NAMESPACE = "harvester-system"


class VersionManager(BaseManager):
    PATH_fmt = "apis/harvesterhci.io/v1beta1/namespaces/{namespace}/versions/{name}"

    API_PATH_fmt = "v1/harvester/harvesterhci.io.versions/{namespace}{name}"

    def get(self, name="", namespace=DEFAULT_HARVESTER_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(name=name, namespace=namespace)
        return self._get(path, raw=raw, **kwargs)

    def create_data(self, name, url, checksum):
        data = {
            "metadata": {
                "name": name,
                "namespace": "harvester-system"
            },
            "spec": {
                "isoChecksum": checksum,
                "isoURL": url
            }
        }
        return data

    def create(self, name, url, checksum, namespace=DEFAULT_HARVESTER_NAMESPACE, *, raw=False):
        data = self.create_data(name, url, checksum)
        path = self.API_PATH_fmt.format(name="", namespace=namespace)
        return self._create(path, json=data, raw=raw)

    def delete(self, name, namespace=DEFAULT_HARVESTER_NAMESPACE, *, raw=False):
        path = self.API_PATH_fmt.format(name=f"/{name}", namespace=namespace)
        return self._delete(path, raw=raw)


class UpgradeManager(BaseManager):
    PATH_fmt = "apis/harvesterhci.io/v1beta1/namespaces/{namespace}/upgrades/{name}"

    CREATE_PATH = "v1/harvester/harvesterhci.io.upgrades"
    API_PATH_fmt = "v1/harvester/harvesterhci.io.upgrades/{namespace}{name}"

    def create_data(self, version_name, namespace=DEFAULT_HARVESTER_NAMESPACE, annotations=None):
        annotations = annotations or dict()
        data = {
            "type": "harvesterhci.io.upgrade",
            "annotations": annotations,
            "metadata": {
                "generateName": "hvst-upgrade-",
                "namespace": namespace
            },
            "spec": {
                "version": version_name
            }
        }
        return data

    def create(
        self, version_name, namespace=DEFAULT_HARVESTER_NAMESPACE, *, raw=False, annotations=None
    ):
        data = self.create_data(version_name, annotations=annotations)
        path = self.API_PATH_fmt.format(name="", namespace=namespace)
        return self._create(path, json=data, raw=raw)

    def get(self, name="", namespace=DEFAULT_HARVESTER_NAMESPACE, *, raw=False, **kwargs):
        path = self.PATH_fmt.format(name=name, namespace=namespace)
        return self._get(path, raw=raw, **kwargs)

    def delete(self, name, namespace=DEFAULT_HARVESTER_NAMESPACE, *, raw=False):
        path = self.API_PATH_fmt.format(name=f"/{name}", namespace=f"{namespace}")
        return self._delete(path, raw=raw)
