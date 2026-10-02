"""
Image Component - delegates to CRD or REST implementation
"""
import os

from constant import HarvesterOperationStrategy, DEFAULT_NAMESPACE
from image.rest import Rest
from image.crd import CRD


class Image:
    """Image component - selects implementation by HARVESTER_OPERATION_STRATEGY"""
    def __init__(self):
        strategy_str = os.getenv("HARVESTER_OPERATION_STRATEGY", "crd").lower()
        if strategy_str == HarvesterOperationStrategy.REST.value:
            self.image = Rest()
        else:
            self.image = CRD()

    def create_from_url(self, image_name, image_url, checksum="", **kwargs):
        return self.image.create_from_url(image_name, image_url, checksum, **kwargs)

    def wait_for_downloaded(self, image_name, timeout):
        return self.image.wait_for_downloaded(image_name, timeout)

    def wait_for_ready(self, image_name, timeout):
        return self.image.wait_for_ready(image_name, timeout)

    def try_create(self, image_name, image_url="", source_type="download", checksum=""):
        return self.image.try_create(image_name, image_url, source_type, checksum)

    def try_get(self, image_name, namespace=DEFAULT_NAMESPACE):
        return self.image.try_get(image_name, namespace)

    def try_delete(self, image_name, namespace=DEFAULT_NAMESPACE):
        return self.image.try_delete(image_name, namespace)

    def update(self, image_name, metadata, namespace=DEFAULT_NAMESPACE):
        return self.image.update(image_name, metadata, namespace)

    def get_metadata(self, image_name, namespace=DEFAULT_NAMESPACE):
        return self.image.get_metadata(image_name, namespace)

    def delete(self, image_name, namespace=DEFAULT_NAMESPACE):
        return self.image.delete(image_name, namespace)

    def wait_for_deleted(self, image_name, timeout):
        return self.image.wait_for_deleted(image_name, timeout)

    def get_status(self, image_name, namespace):
        return self.image.get_status(image_name, namespace)

    def list(self, namespace):
        return self.image.list(namespace)

    def exists(self, image_name, namespace):
        return self.image.exists(image_name, namespace)

    def cleanup(self):
        return self.image.cleanup()
