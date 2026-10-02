from .base import BaseManager, merge_dict


class NamespaceManager(BaseManager):
    PATH_fmt = "v1/harvester/namespaces{uid}"

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=f"/{name}"), raw=raw)

    def create(self, name, description=None, labels=None, annotations=None, *, raw=False):
        data = {
            "type": "namespace",
            "disableOpenApiValidation": 'false',
            "metadata": {
                "annotations": annotations,
                "labels": labels,
                "name": name,
            }
        }
        return self._create(self.PATH_fmt.format(uid=""), json=data, raw=raw)

    def update(self, name, metadata, merge=True, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(uid=f"/{name}")
        _, node = self.get(name)
        name = node['metadata']['labels']["kubernetes.io/metadata.name"]
        if merge:
            merge_dict(metadata, node['metadata'])
        else:
            for field in ('annotations', "labels"):
                if metadata.get(field) is not None:
                    node['metadata'][field] = metadata[field]
        # keep name immutable
        node['metadata']['name'] = node['metadata']['labels']["kubernetes.io/metadata.name"] = name
        return self._update(path, node, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{name}")
        return self._delete(path, raw=raw)
