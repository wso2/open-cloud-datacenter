
"""
Image Keywords - creates Image() instance and delegates - NO direct API calls!
"""
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging # noqa E402
from image import Image # noqa E402
from constant import DEFAULT_TIMEOUT # noqa E402


class image_keywords:
    """Layer 3: Image keyword wrapper - creates Image component and delegates"""

    def __init__(self):
        self.image = Image()

    def cleanup_images(self):
        """Clean up all test images"""
        self.image.cleanup()

    def create_image_from_url(self, image_name, image_url, checksum="", **kwargs):
        """Create image from URL"""
        logging(f'Creating image {image_name}')
        self.image.create_from_url(image_name, image_url, checksum, **kwargs)

    def wait_for_image_downloaded(self, image_name, timeout=DEFAULT_TIMEOUT):
        """Wait for image to be downloaded"""
        logging(f'Waiting for image {image_name} to be downloaded')
        self.image.wait_for_downloaded(image_name, timeout)

    def wait_for_image_ready(self, image_name, timeout=DEFAULT_TIMEOUT):
        """Wait for image to be ready"""
        logging(f'Waiting for image {image_name} to be ready')
        self.image.wait_for_ready(image_name, timeout)

    def delete_image(self, image_name, namespace='default'):
        """Delete an image"""
        logging(f'Deleting image {image_name}')
        self.image.delete(image_name, namespace)

    def wait_for_image_deleted(self, image_name, timeout=DEFAULT_TIMEOUT):
        """Wait for image to be deleted"""
        logging(f'Waiting for image {image_name} to be deleted')
        self.image.wait_for_deleted(image_name, timeout)

    def get_image_status(self, image_name, namespace='default'):
        """Get image status"""
        logging(f'Getting status for image {image_name}')
        return self.image.get_status(image_name, namespace)

    def list_images(self, namespace='default'):
        """List all images"""
        logging('Listing all images')
        return self.image.list(namespace)

    def image_exists(self, image_name, namespace='default'):
        """Check if image exists"""
        return self.image.exists(image_name, namespace)

    def try_create_image(self, image_name, image_url="", source_type="download",
                         checksum=""):
        """Attempt to create an image for negative testing; returns result dict"""
        logging(f'Attempting to create image {image_name} '
                f'(sourceType={source_type}, url={image_url}) (negative test)')
        return self.image.try_create(image_name, image_url, source_type, checksum)

    def try_get_image(self, image_name, namespace='default'):
        """Attempt to get an image for negative testing; returns result dict"""
        logging(f'Attempting to get image {image_name} (negative test)')
        return self.image.try_get(image_name, namespace)

    def try_delete_image(self, image_name, namespace='default'):
        """Attempt to delete an image for negative testing; returns result dict"""
        logging(f'Attempting to delete image {image_name} (negative test)')
        return self.image.try_delete(image_name, namespace)

    def update_image(self, image_name, metadata, namespace='default'):
        """Update an image's metadata (labels/annotations)"""
        logging(f'Updating image {image_name} metadata')
        return self.image.update(image_name, metadata, namespace)

    def get_image_metadata(self, image_name, namespace='default'):
        """Return the metadata block of an image"""
        return self.image.get_metadata(image_name, namespace)
