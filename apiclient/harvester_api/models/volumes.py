from copy import deepcopy
from math import ceil


class VolumeSpec:
    volume_mode = "Block"
    _data = None

    def __init__(self, size, storage_cls=None, description=None, annotations=None):
        self.access_modes = ["ReadWriteMany"]

        self.size = size
        self.storage_cls = storage_cls
        self.description = description
        self.annotations = annotations or dict()

    def to_dict(self, name, namespace, image_id=None, image_uid=None):
        annotations = self.annotations
        size = f"{self.size}Gi" if isinstance(self.size, (int, float)) else self.size
        data = {
            "type": "persistentvolumeclaim",
            "metadata": {
                "namespace": namespace,
                "name": name,
                "annotations": annotations
            },
            "spec": {
                "accessModes": self.access_modes,
                "resources": {
                    "requests": dict(storage=size)
                },
                "volumeMode": self.volume_mode
            }
        }

        if image_id is not None:
            annotations.update({"harvesterhci.io/imageId": image_id})
            try:
                self.storage_cls = f"longhorn-{image_id.split('/')[1]}"
            except IndexError:
                self.storage_cls = f"longhorn-{image_id}"
        if self.description is not None:
            annotations.update({"field.cattle.io/description": self.description})
        if self.storage_cls is not None:
            data['spec']['storageClassName'] = self.storage_cls

        if self._data:
            self._data['metadata'].update(data['metadata'])
            self._data['spec'].update(data['spec'])
            return deepcopy(self._data)

        return deepcopy(data)

    @classmethod
    def from_dict(cls, data):
        spec, metadata = data.get('spec', {}), data.get('metadata', {})
        annotations = metadata.get('annotations')
        size = spec['resources']['requests']['storage']
        storage_cls = spec.get('storageClassName')

        obj = cls(size, storage_cls, annotations=annotations)
        obj._data = data

        return obj

    @classmethod
    def for_image(cls, image_info, size=None, description=None, annotations=None):
        size = size or ceil(image_info['status']['virtualSize'] / 1024 ** 3)  # convert to GiB
        image_id = f"{image_info['metadata']['namespace']}/{image_info['metadata']['name']}"
        anno = annotations or dict()
        anno.update({"harvesterhci.io/imageId": image_id})
        storage_cls = image_info['status']['storageClassName']
        return cls(size, storage_cls, description, anno)


class VolumeSpec180(VolumeSpec):
    # ref: https://github.com/harvester/harvester/issues/5165

    def to_dict(self, name, namespace, image_id=None, image_uid=None):
        if image_id is not None:
            self.annotations.update({"harvesterhci.io/imageId": image_id})
            image_id = None
        if image_uid is not None:
            self.storage_cls = f"lh-{image_uid}"
        return super().to_dict(name, namespace, image_id)
