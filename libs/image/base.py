"""
Base class for Image operations (optional - for common patterns)
"""
from abc import ABC, abstractmethod


class Base(ABC):
    """Base class for Image implementations"""

    @abstractmethod
    def create_from_url(self, image_name, image_url, checksum="", **kwargs):
        """Create image from URL"""
        pass

    @abstractmethod
    def delete(self, image_name, namespace="default"):
        """Delete an image"""
        pass

    @abstractmethod
    def get_status(self, image_name, namespace):
        """Get image status"""
        pass

    @abstractmethod
    def list(self, namespace):
        """List all images in the specified namespace"""
        pass

    @abstractmethod
    def exists(self, image_name, namespace):
        """Check if an image exists in the specified namespace"""
        pass

    @abstractmethod
    def wait_for_downloaded(self, image_name, timeout):
        """Wait for the image to be fully downloaded"""
        pass

    @abstractmethod
    def wait_for_ready(self, image_name, timeout):
        """Wait for the image to be ready"""
        pass

    @abstractmethod
    def cleanup(self):
        """Clean up all test images"""
        pass
