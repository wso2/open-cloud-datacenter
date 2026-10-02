import base64
from .base import BaseManager, DEFAULT_NAMESPACE


class SecretManager(BaseManager):
    PATH_fmt = "/api/v1/namespaces/{ns}/secrets/{name}"
    CREATE_fmt = "v1/harvester/secrets"

    def create_data(self, name, namespace, data, annotations=None):
        annotations = annotations or {}
        encoded_data = {
            k: base64.b64encode(v.encode('utf-8')).decode('utf-8')
            for k, v in data.items()
        }

        return {
            "type": "secret",
            "metadata": {
                "namespace": namespace,
                "name": name,
                "annotations": annotations
            },
            "data": encoded_data
        }

    def create(self, name, data, namespace=DEFAULT_NAMESPACE, annotations=None, **kwargs):
        secret_data = self.create_data(name, namespace, data, annotations=annotations)
        return self._create(self.CREATE_fmt, json=secret_data, **kwargs)

    def get(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._get(self.PATH_fmt.format(ns=namespace, name=name), raw=raw)

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._delete(self.PATH_fmt.format(ns=namespace, name=name), raw=raw)
