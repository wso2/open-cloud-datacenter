from .base import BaseManager, merge_dict

DEFAULT_STORAGE_CLASS_ANNOTATION = "storageclass.kubernetes.io/is-default-class"


class StorageClassManager(BaseManager):
    API_VERSION = "storage.k8s.io"
    CREATE_PATH_fmt = "v1/harvester/{SC_API}.storageclasses"
    PATH_fmt = "/apis/{SC_API}/v1/storageclasses/{name}"

    default = "harvester-longhorn"

    def get(self, name="", *, raw=False, **kwargs):
        path = self.PATH_fmt.format(SC_API=self.API_VERSION, name=name)
        return self._get(path, raw=raw, **kwargs)

    def get_default(self):
        code, data = self.get()
        for sc in data['items']:
            if 'true' == sc['metadata'].get('annotations', {}).get(
              DEFAULT_STORAGE_CLASS_ANNOTATION):
                return code, sc
        else:
            return code, data

    def create_data(self, name, replicas, **options):
        data = {
            "type": f"{self.API_VERSION}",
            "metadata": {
                "name": name
            },
            "parameters": {
                "numberOfReplicas": f"{replicas}",
                "staleReplicaTimeout": "30",
                "migratable": "true"
            },
            "provisioner": "driver.longhorn.io",
            "allowVolumeExpansion": True,
            "reclaimPolicy": "Delete",
            "volumeBindingMode": "Immediate"
        }
        return merge_dict(options, data)

    def create(self, name, replicas=3, *, raw=False, **options):
        path = self.CREATE_PATH_fmt.format(SC_API=self.API_VERSION)
        data = self.create_data(name, replicas, **options)
        return self._create(path, json=data, raw=raw)

    def set_default(self, name, *, raw=False):
        code, resp = self.get()

        if code == 200 and len(resp['items']):
            for sc in resp['items']:
                if sc['metadata'].get('annotations', {}).get(
                   DEFAULT_STORAGE_CLASS_ANNOTATION) == 'true':

                    if sc['metadata']['name'] == name:
                        return (200, sc)

                    # reset old default storage class
                    path = self.PATH_fmt.format(name=sc['metadata']['name'],
                                                SC_API=self.API_VERSION)
                    resp = self._patch(path, json={
                        "metadata": {
                            "annotations": {
                                "storageclass.kubernetes.io/is-default-class": "false",
                                "storageclass.beta.kubernetes.io/is-default-class": "false"
                            }
                        }
                    }, raw=raw)

                    break

        path = self.PATH_fmt.format(name=name, SC_API=self.API_VERSION)
        rv = self._patch(path, json={
            "metadata": {
                "annotations": {
                    "storageclass.kubernetes.io/is-default-class": "true",
                    "storageclass.beta.kubernetes.io/is-default-class": "true"
                }
            }
        }, raw=raw)

        try:
            code, data = rv
        except TypeError:
            code = rv.status_code

        if code == 200:
            self.default = name
        return rv

    def delete(self, name, *, raw=False):
        path = self.PATH_fmt.format(name=name, SC_API=self.API_VERSION)
        return self._delete(path, raw=raw)
