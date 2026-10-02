from .base import BaseManager


class SupportBundleManager(BaseManager):
    PATH_fmt = ("apis/{{API_VERSION}}/namespaces/harvester-system"
                "/supportbundles/{uid}")
    DL_fmt = "/v1/harvester/supportbundles/{uid}/download"

    def create_data(self, name, description, issue_url, **specs):
        data = {
            "apiVersion": "{API_VERSION}",
            "kind": "SupportBundle",
            "metadata": {
                "generateName": f"{name}-",
                "namespace": "harvester-system"
            },
            "spec": {
                "description": description,
                "issueURL": issue_url
            }
        }

        available_specs = ("timeout", "nodeTimeout", "expiration", "extraCollectionNamespaces")
        data['spec'].update({
            k: specs.get(k) for k in available_specs
            if k in specs and specs.get(k) is not None
        })

        return self._inject_data(data)

    def get(self, uid="", *, raw=False):
        path = self.PATH_fmt.format(uid=uid)
        return self._get(path, raw=raw)

    def create(self, name, description="", issue_url="",
               timeout=None, node_timeout=None, expiration=None,
               extra_collection_namespaces=None, *, raw=False):
        data = self.create_data(
            name, description, issue_url,
            timeout=timeout,
            nodeTimeout=node_timeout,
            expiration=expiration,
            extraCollectionNamespaces=extra_collection_namespaces
        )
        path = self.PATH_fmt.format(uid="")

        return self._create(path, json=data, raw=raw)

    def download(self, uid):
        path = self.DL_fmt.format(uid=uid)
        resp = self._get(path, raw=True)

        return resp.status_code, resp.content

    def update(self, *args, **kwargs):
        raise NotImplementedError("Update Support Bundle is not allowed")

    def delete(self, uid, *, raw=False):
        path = self.PATH_fmt.format(uid=uid)
        return self._delete(path, raw=raw)
