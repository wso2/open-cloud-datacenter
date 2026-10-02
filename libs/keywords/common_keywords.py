
"""
Common Keywords
"""
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.pod import get_pods_by_label  # noqa E402
from utility.utility import generate_name_with_suffix  # noqa E402
from utility.utility import init_harvester_api_client  # noqa E402
from utility.utility import init_k8s_api_client  # noqa E402
from utility.utility import logging  # noqa E402
from utility.ssh import generate_ssh_keypair  # noqa E402
from constant import LONGHORN_NAMESPACE  # noqa E402


class common_keywords:
    """Layer 3: Wrapper keywords - NO API client stored here!"""

    def __init__(self):
        pass

    def init_harvester_api_client(self, endpoint, username, password):
        """Initialize Harvester API client"""
        return init_harvester_api_client(endpoint, username, password)

    def init_k8s_api_client(self):
        """Initialize Kubernetes API client"""
        return init_k8s_api_client()

    def generate_name_with_suffix(self, kind, suffix, precise=False):
        """Generate unique name with timestamp"""
        return generate_name_with_suffix(kind, suffix, precise)

    def cluster_version_is_at_least(self, version):
        """
        Check the cluster release version against a lower bound

        Args:
            version: Version string like '1.9.0'

        Returns:
            bool: True if the cluster release >= version. Versionless dev
                  builds (master/commit-hash) are treated as the newest.
        """
        from utility.utility import get_cluster_version_release, _extract_release
        release = get_cluster_version_release()
        if release is None:
            return True
        target = _extract_release(str(version))
        assert target is not None, f"Not a version string: {version}"
        return release >= target

    def generate_ssh_keypair(self):
        """Generate a fresh RSA keypair for injecting into a VM's cloud-init
        ssh_authorized_keys (public) and guest SSH login (private).
        Returns: (public_key, private_key)
        """
        return generate_ssh_keypair()

    def cleanup_vms(self):
        """Cleanup VMs"""
        from vm import VM
        VM().cleanup()

    def cleanup_images(self):
        """Cleanup images"""
        from image import Image
        Image().cleanup()

    def cleanup_volumes(self):
        """Cleanup volumes"""
        from volume import Volume
        Volume().cleanup()

    def cleanup_storageclasses(self):
        """Cleanup storageclasses"""
        from storageclass import StorageClass
        StorageClass().cleanup()

    def cleanup_networks(self):
        """Cleanup networks"""
        from network import Network
        net = Network()
        net.cleanup_vlan_networks()
        net.cleanup_vlan_configs()
        net.cleanup_cluster_networks()

    def cleanup_namespaces(self):
        """Cleanup namespaces"""
        from namespace import Namespace
        Namespace().cleanup()

    def cleanup_projects(self):
        """Cleanup projects"""
        from project import Project
        Project().cleanup()

    def cleanup_backups(self):
        """Cleanup backups"""
        logging('Cleanup backups requested')

    def list_pods_by_label(self, namespace, label_selector, status=None):
        """List pods by label
        Returns: list of V1Pod objects
        """
        pods = get_pods_by_label(namespace, label_selector)
        if status:
            pods = [pod for pod in pods if pod.status.phase == status]
        logging(f"Found {len(pods)} pods by label {label_selector} in namespace {namespace}: \
                {[pod.metadata.name for pod in pods]}")
        return pods

    def list_running_im_pods(self):
        """List running instance-manager pods
        Returns: list of V1Pod objects
        """
        return self.list_pods_by_label(
            namespace=LONGHORN_NAMESPACE,
            label_selector='longhorn.io/data-engine=v1,longhorn.io/component=instance-manager',
            status='Running'
        )
