from .base import DEFAULT_NAMESPACE, BaseManager


class KeypairManager(BaseManager):
    PATH_fmt = "apis/{{API_VERSION}}/namespaces/{ns}/keypairs/{uid}"
    _KIND = "KeyPair"

    def create_data(self, name, namespace, public_key):
        data = {
            "apiVersion": "{API_VERSION}",
            "kind": self._KIND,
            "metadata": {
                "name": name,
                "namespace": namespace
            },
            "spec": {
                "publicKey": public_key
            }
        }

        return self._inject_data(data)

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name, ns=namespace), raw=raw)

    def create(self, name, public_key, namespace=DEFAULT_NAMESPACE, *, raw=False):
        data = self.create_data(name, namespace, public_key)
        return self._create(self.PATH_fmt.format(uid="", ns=namespace), json=data, raw=raw)

    def update(self, *args, **kwargs):
        raise NotImplementedError("Update Keypairs is not allowed")

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=name, ns=namespace)
        return self._delete(path, raw=raw)
