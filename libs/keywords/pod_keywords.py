"""
Pod Keywords - creates Pod() instance and delegates - NO direct API calls!
"""
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa: E402

from utility.utility import logging  # noqa: E402
from pod import Pod  # noqa: E402
from constant import DEFAULT_TIMEOUT_SHORT  # noqa: E402


class pod_keywords:
    """Layer 3: Pod keyword wrapper - creates Pod component and delegates."""

    def __init__(self):
        self.pod = Pod()

    def create_pod(self, pod_name, namespace, spec):
        """Create a pod from a spec dict (idempotent — deletes any existing pod first)."""
        logging(f"Creating pod '{pod_name}' in namespace '{namespace}'")
        self.pod.create(pod_name, namespace, spec)

    def wait_for_pod_succeeded(self, pod_name, namespace, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait until the pod reaches the Succeeded phase."""
        logging(f"Waiting for pod '{pod_name}' to succeed")
        self.pod.wait_for_succeeded(pod_name, namespace, timeout)

    def get_pod_logs(self, pod_name, namespace):
        """Return the full log output of a pod as a string."""
        logging(f"Getting logs for pod '{pod_name}'")
        return self.pod.get_logs(pod_name, namespace)

    def delete_pod_if_exists(self, pod_name, namespace):
        """Delete a pod if it exists; silently ignores 404."""
        logging(f"Deleting pod '{pod_name}' if it exists")
        self.pod.delete_if_exists(pod_name, namespace)

    def wait_for_pods_running_with_label_selector(
        self, namespace, label_selector, timeout=DEFAULT_TIMEOUT_SHORT
    ):
        logging(f"Waiting for pod with label selector '{label_selector}' to be running")
        return self.pod.wait_for_running_with_label_selector(namespace, label_selector, timeout)

    def wait_for_pods_gone_with_label_selector(
        self, namespace, label_selector, timeout=DEFAULT_TIMEOUT_SHORT
    ):
        logging(f"Waiting for pod with label selector '{label_selector}' to be gone")
        return self.pod.wait_for_gone_with_label_selector(namespace, label_selector, timeout)
