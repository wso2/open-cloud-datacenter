from pathlib import Path
from collections.abc import Mapping

from .base import DEFAULT_NAMESPACE, BaseManager, merge_dict


class ImageManager(BaseManager):
    # get, create, update, delete
    PATH_fmt = "apis/{{API_VERSION}}/namespaces/{ns}/virtualmachineimages/{uid}"
    UPLOAD_fmt = "v1/harvester/harvesterhci.io.virtualmachineimages/{ns}/{uid}"
    DOWNLOAD_fmt = "v1/harvester/harvesterhci.io.virtualmachineimages/{ns}/{uid}/download"
    _KIND = "VirtualMachineImage"

    def create_data(self, name, url, desc, stype, namespace, image_checksum=None,
                    display_name=None, storageclass=None):
        data = {
            "apiVersion": "{API_VERSION}",
            "kind": self._KIND,
            "metadata": {
                "annotations": {
                    "harvesterhci.io/storageClassName": storageclass or self.api.scs.default,
                    "field.cattle.io/description": desc
                },
                "name": name,
                "namespace": namespace
            },
            "spec": {
                "displayName": display_name or name,
                "sourceType": stype,
                "checksum": image_checksum,
                "url": url
            }
        }
        return self._inject_data(data)

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name, ns=namespace), raw=raw)

    def create(self, name, namespace=DEFAULT_NAMESPACE, **kwargs):
        return self._create(self.PATH_fmt.format(uid=name, ns=namespace), **kwargs)

    def create_by_url(
        self, name, url, imageChecksum=None, namespace=DEFAULT_NAMESPACE,
        description="", display_name=None, storageclass=None
    ):
        data = self.create_data(name, url, description, "download", namespace, imageChecksum,
                                display_name, storageclass)
        return self.create("", namespace, json=data)

    def create_by_file(
        self, name, filepath, namespace=DEFAULT_NAMESPACE,
        description="", display_name=None, storageclass=None
    ):
        file = Path(filepath).expanduser()

        data = self.create_data(name, "", description, "upload", namespace,
                                display_name, storageclass)
        self.create("", namespace, json=data)

        kwargs = {
            "params": dict(action="upload", size=file.stat().st_size),
            "files": dict(chunk=file.open('rb')),
        }
        return self._create(self.UPLOAD_fmt.format(uid=name, ns=namespace), raw=True, **kwargs)

    def update(self, name, data, *, raw=False, as_json=True, **kwargs):
        if isinstance(data, Mapping) and as_json:
            _, curr = self.get(name)
            data = merge_dict(data, curr)

        ns = data.get("metadata", {}).get("namespace", DEFAULT_NAMESPACE)
        path = self.PATH_fmt.format(uid=name, ns=ns)
        return self._update(path, data, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name, ns=namespace))

    def download(self, name, namespace=DEFAULT_NAMESPACE):
        return self._get(self.DOWNLOAD_fmt.format(uid=name, ns=namespace), raw=True)

    def create_by_encrypt(
        self,
        source_image_name,
        new_image_name,
        storage_class_name,
        namespace=DEFAULT_NAMESPACE,
    ):
        data = self.create_data(
            name=new_image_name,
            url=None,
            desc="",
            stype="clone",
            namespace=namespace,
            image_checksum=None,
            display_name=new_image_name,
            storageclass=storage_class_name
        )

        # Add securityParameters
        data["spec"]["securityParameters"] = {
            "cryptoOperation": "encrypt",
            "sourceImageName": source_image_name,
            "sourceImageNamespace": namespace,
        }

        return self.create("", namespace=namespace, json=data)

    def create_by_decrypt(
        self,
        source_image_name,
        new_image_name,
        storage_class_name,
        namespace=DEFAULT_NAMESPACE,
    ):
        data = self.create_data(
            name=new_image_name,
            url=None,
            desc="",
            stype="clone",
            namespace=namespace,
            image_checksum=None,
            display_name=new_image_name,
            storageclass=storage_class_name
        )

        # Add securityParameters
        data["spec"]["securityParameters"] = {
            "cryptoOperation": "decrypt",
            "sourceImageName": source_image_name,
            "sourceImageNamespace": namespace,
        }

        return self.create("", namespace=namespace, json=data)
