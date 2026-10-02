from .base import DEFAULT_NAMESPACE, BaseManager


class VolumeSnapshotManager(BaseManager):
    PATH_fmt = "v1/harvester/snapshot.storage.k8s.io.volumesnapshots/{ns}/{uid}"

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=name, ns=namespace)
        return self._delete(path, raw=raw)

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=name, ns=namespace)
        rv = self._get(path, raw=raw)
        if not (name or raw):
            _, ds = rv
            ds['data'] = [
                d for d in ds['data']
                if d['metadata']['ownerReferences'][0]['kind'] == "PersistentVolumeClaim"
            ]
        return rv

    def restore(self, name, vol_name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=namespace)
        params = dict(action="restore")
        spec = dict(name=vol_name)
        return self._create(path, params=params, json=spec, raw=raw)
