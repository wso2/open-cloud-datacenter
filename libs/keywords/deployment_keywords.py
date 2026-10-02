"""
Deployment Keywords for Robot Framework
Wraps generic deployment lifecycle operations.
"""
import os
import sys
import time

from kubernetes import client  # noqa: E402
from kubernetes.client.rest import ApiException  # noqa: E402

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging, get_retry_count_and_interval  # noqa: E402
from constant import DEFAULT_TIMEOUT  # noqa: E402


class deployment_keywords:
    """Robot Framework keyword library for generic deployment lifecycle operations."""

    def __init__(self):
        self._apps_api = None
        self.retry_count, self.retry_interval = get_retry_count_and_interval()

    @property
    def apps_api(self):
        if self._apps_api is None:
            self._apps_api = client.AppsV1Api()
        return self._apps_api

    def wait_for_deployment_ready(self, name, namespace, timeout=DEFAULT_TIMEOUT):
        """
        Wait for a Deployment to report all replicas ready

        Args:
            name: Name of the deployment
            namespace: Kubernetes namespace
            timeout: Timeout in seconds
        """
        logging(f"Waiting for deployment '{namespace}/{name}' to be ready")
        max_retries = int(timeout / self.retry_interval)

        for i in range(max_retries):
            try:
                deployment = self.apps_api.read_namespaced_deployment(
                    name=name, namespace=namespace
                )
                desired = deployment.spec.replicas or 0
                ready = deployment.status.ready_replicas or 0
                if desired > 0 and ready == desired:
                    logging(f"Deployment '{namespace}/{name}' is ready ({ready}/{desired})")
                    return True
                logging(
                    f"Deployment '{namespace}/{name}' not ready ({ready}/{desired}), "
                    f"retrying... ({i+1}/{max_retries})"
                )
            except ApiException as e:
                if e.status == 404:
                    logging(
                        f"Deployment '{namespace}/{name}' not found, retrying... "
                        f"({i+1}/{max_retries})"
                    )
                else:
                    logging(f"Error reading deployment: {e}", level='WARNING')

            time.sleep(self.retry_interval)

        raise TimeoutError(
            f"Timeout waiting for deployment '{namespace}/{name}' to be ready after {timeout}s"
        )

    def wait_for_deployment_gone(self, name, namespace, timeout=DEFAULT_TIMEOUT):
        """
        Wait for a Deployment to be removed

        Args:
            name: Name of the deployment
            namespace: Kubernetes namespace
            timeout: Timeout in seconds
        """
        logging(f"Waiting for deployment '{namespace}/{name}' to be gone")
        max_retries = int(timeout / self.retry_interval)

        for i in range(max_retries):
            try:
                self.apps_api.read_namespaced_deployment(name=name, namespace=namespace)
                logging(
                    f"Deployment '{namespace}/{name}' still present, retrying... "
                    f"({i+1}/{max_retries})"
                )
            except ApiException as e:
                if e.status == 404:
                    logging(f"Deployment '{namespace}/{name}' is gone")
                    return True
                logging(f"Error reading deployment: {e}", level='WARNING')

            time.sleep(self.retry_interval)

        raise TimeoutError(
            f"Timeout waiting for deployment '{namespace}/{name}' to be gone after {timeout}s"
        )
