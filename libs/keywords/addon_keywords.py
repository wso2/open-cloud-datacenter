"""
Addon Keywords - creates Addon() instance and delegates - NO direct API calls!
Layer 3: Keyword wrappers for Robot Framework
"""
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging  # noqa E402
from addon import Addon  # noqa E402
from constant import DEFAULT_TIMEOUT, DEFAULT_TIMEOUT_LONG  # noqa E402


class addon_keywords:
    """Addon keyword wrapper - creates Addon component and delegates"""
    def __init__(self):
        """Initialize addon keywords with lazy loading"""
        self._addon = None

    @property
    def addon(self):
        """Lazy initialize addon to allow API client setup first"""
        if self._addon is None:
            self._addon = Addon()
        return self._addon

    def get_addon(self, addon_name):
        """
        Get addon details

        Args:
            addon_name: Name of the addon

        Returns:
            dict: Addon object
        """
        logging(f'Getting addon {addon_name}')
        return self.addon.get_addon(addon_name)

    def get_addon_initial_state(self, addon_name):
        """
        Get initial state of addon (enabled/disabled)

        Args:
            addon_name: Name of the addon

        Returns:
            bool: True if addon is enabled, False otherwise
        """
        logging(f'Getting initial state of addon {addon_name}')
        return self.addon.is_addon_enabled(addon_name)

    def enable_addon(self, addon_name):
        """
        Enable an addon

        Args:
            addon_name: Name of the addon to enable
        """
        logging(f'Enabling addon {addon_name}')
        self.addon.enable_addon(addon_name)

    def disable_addon(self, addon_name):
        """
        Disable an addon

        Args:
            addon_name: Name of the addon to disable
        """
        logging(f'Disabling addon {addon_name}')
        self.addon.disable_addon(addon_name)

    def wait_for_addon_enabled(self, addon_name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for addon to be enabled

        Args:
            addon_name: Name of the addon
            timeout: Timeout in seconds
        """
        logging(f'Waiting for addon {addon_name} to be enabled')
        self.addon.wait_for_addon_enabled(addon_name, int(timeout))

    def wait_for_addon_disabled(self, addon_name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for addon to be disabled

        Args:
            addon_name: Name of the addon
            timeout: Timeout in seconds
        """
        logging(f'Waiting for addon {addon_name} to be disabled')
        self.addon.wait_for_addon_disabled(addon_name, int(timeout))

    def get_addon_status(self, addon_name):
        """
        Get addon status

        Args:
            addon_name: Name of the addon

        Returns:
            dict: Addon status
        """
        logging(f'Getting addon {addon_name} status')
        return self.addon.get_addon_status(addon_name)

    def is_addon_enabled(self, addon_name):
        """
        Check if addon is enabled

        Args:
            addon_name: Name of the addon

        Returns:
            bool: True if addon is enabled, False otherwise
        """
        return self.addon.is_addon_enabled(addon_name)

    def wait_for_service_running(self, namespace, service_name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for service to be running in namespace

        Args:
            namespace: Kubernetes namespace
            service_name: Name of the service
            timeout: Timeout in seconds
        """
        logging(f'Waiting for service {service_name} in namespace {namespace} to be running')
        self.addon.wait_for_service_running(namespace, service_name, int(timeout))

    def get_configmap_data(self, name, namespace):
        """
        Get the data map of a ConfigMap

        Args:
            name: Name of the ConfigMap
            namespace: Kubernetes namespace

        Returns:
            dict: The ConfigMap's data, or None if the ConfigMap does not exist
        """
        logging(f'Getting ConfigMap {namespace}/{name}')
        return self.addon.get_configmap_data(name, namespace)

    def get_addon_values_content(self, addon_name):
        """
        Get the raw spec.valuesContent string of an addon

        Args:
            addon_name: Name of the addon

        Returns:
            str: The raw valuesContent (empty string when unset)
        """
        logging(f'Getting raw valuesContent of addon {addon_name}')
        return self.addon.get_addon_values_content(addon_name)

    def get_addon_values(self, addon_name):
        """
        Get the parsed spec.valuesContent of an addon

        Args:
            addon_name: Name of the addon

        Returns:
            dict: Parsed valuesContent (empty dict when unset)
        """
        logging(f'Getting parsed valuesContent of addon {addon_name}')
        return self.addon.get_addon_values(addon_name)

    def set_addon_values_content(self, addon_name, values_content):
        """
        Replace the raw spec.valuesContent of an addon

        Args:
            addon_name: Name of the addon
            values_content: Raw YAML string to store
        """
        logging(f'Setting raw valuesContent of addon {addon_name}')
        self.addon.set_addon_values_content(addon_name, values_content)

    def update_addon_values(self, addon_name, values):
        """
        Replace spec.valuesContent of an addon from a dict

        Args:
            addon_name: Name of the addon
            values: dict serialised to YAML and stored as valuesContent
        """
        logging(f'Updating valuesContent of addon {addon_name}')
        self.addon.update_addon_values(addon_name, values)

    def get_addon_labels(self, addon_name):
        """
        Get the metadata.labels of an addon

        Args:
            addon_name: Name of the addon

        Returns:
            dict: The addon's labels (empty dict when unset)
        """
        addon = self.addon.get_addon(addon_name)
        if not addon:
            raise Exception(f"Addon {addon_name} not found")
        return addon.get('metadata', {}).get('labels', {}) or {}

    def get_addon_spec(self, addon_name):
        """
        Get the spec of an addon

        Args:
            addon_name: Name of the addon

        Returns:
            dict: The addon's spec (empty dict when unset)
        """
        addon = self.addon.get_addon(addon_name)
        if not addon:
            raise Exception(f"Addon {addon_name} not found")
        return addon.get('spec', {}) or {}

    def addon_exists(self, addon_name):
        """
        Check whether an addon CR exists in any known addon namespace

        Older Harvester releases do not ship every addon, so suites use this to
        skip rather than fail.

        Args:
            addon_name: Name of the addon

        Returns:
            bool: True if the addon CR exists
        """
        try:
            return self.addon.get_addon(addon_name) is not None
        except Exception as e:
            logging(f'Error checking whether addon {addon_name} exists: {e}', level='WARNING')
            return False

    def wait_for_monitoring_pods_running(self, namespace, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for monitoring pods to be running

        Args:
            namespace: Kubernetes namespace where monitoring pods are deployed
            timeout: Timeout in seconds
        """
        logging(f'Waiting for monitoring pods in namespace {namespace} to be running')
        # Wait for Prometheus pods
        self.addon.wait_for_pods_running(
            namespace,
            'app.kubernetes.io/name=prometheus',
            int(timeout)
        )
        # Wait for Grafana pods
        self.addon.wait_for_pods_running(
            namespace,
            'app.kubernetes.io/name=grafana',
            int(timeout)
        )

    def port_forward_to_prometheus(self, namespace, pod_name, local_port=9090):
        """
        Port forward to Prometheus pod

        Args:
            namespace: Kubernetes namespace
            pod_name: Name of the Prometheus pod
            local_port: Local port to forward to (default: 9090)
        """
        logging(f'Port forwarding to Prometheus pod {pod_name}')
        self.addon.port_forward(namespace, pod_name, local_port, 9090)

    def stop_port_forward(self):
        """Stop port forwarding"""
        logging('Stopping port forward')
        self.addon.stop_port_forward()

    def query_prometheus(self, query, prometheus_url='http://localhost:9090'):
        """
        Query Prometheus for metrics

        Args:
            query: PromQL query string
            prometheus_url: Prometheus URL (default: http://localhost:9090)

        Returns:
            dict: Query result
        """
        logging(f'Querying Prometheus: {query}')
        return self.addon.query_prometheus(query, prometheus_url)

    def verify_prometheus_metric_exists(
        self, query, prometheus_url='http://localhost:9090', retries=12, retry_interval=5
    ):
        """
        Verify that a Prometheus metric exists with retry logic

        Args:
            query: PromQL query string
            prometheus_url: Prometheus URL (default: http://localhost:9090)
            retries: Number of retry attempts (default: 12)
            retry_interval: Seconds to wait between retries (default: 5)

        Returns:
            bool: True if metric exists and has data
        """
        logging(f'Verifying Prometheus metric: {query}')
        return self.addon.verify_prometheus_metric_exists(
            query, prometheus_url, retries, retry_interval
        )

    def restore_addon_state(self, addon_name, initial_state):
        """
        Restore addon to its initial state

        Args:
            addon_name: Name of the addon
            initial_state: Initial state (True for enabled, False for disabled)
        """
        logging(f'Restoring addon {addon_name} to initial state: {initial_state}')

        # Handle case where initial_state might be None
        if initial_state is None:
            logging(f'Initial state is None, skipping restore for addon {addon_name}')
            return

        current_state = self.addon.is_addon_enabled(addon_name)

        if current_state != initial_state:
            if initial_state:
                self.enable_addon(addon_name)
                self.wait_for_addon_enabled(addon_name)
            else:
                self.disable_addon(addon_name)
                self.wait_for_addon_disabled(addon_name)
        else:
            logging(f'Addon {addon_name} already in desired state')

    def configure_nvidia_toolkit_addon(self, addon_name, image_repo, image_tag, driver_location):
        """
        Configure the nvidia-driver-toolkit addon with image repo, tag, and driver location

        Args:
            addon_name: Name of the addon (nvidia-driver-toolkit)
            image_repo: Image repository
            image_tag: Image tag
            driver_location: Driver location path
        """
        logging(
            f'Configuring nvidia-driver-toolkit addon: '
            f'repo={image_repo}, tag={image_tag}, driver={driver_location}'
        )
        self.addon.configure_nvidia_toolkit(addon_name, image_repo, image_tag, driver_location)
        logging('Nvidia-driver-toolkit addon configured')

    def verify_nvidia_toolkit_addon_config(
        self, addon_name, image_repo, image_tag, driver_location
    ):
        """
        Verify nvidia-driver-toolkit addon configuration values

        Args:
            addon_name: Name of the addon (nvidia-driver-toolkit)
            image_repo: Expected image repository
            image_tag: Expected image tag
            driver_location: Expected driver location path

        Returns:
            bool: True if configuration matches expected values
        """
        logging(
            f'Verifying nvidia-driver-toolkit addon configuration: '
            f'repo={image_repo}, tag={image_tag}, driver={driver_location}'
        )
        return self.addon.verify_nvidia_toolkit_configuration(
            addon_name, image_repo, image_tag, driver_location
        )

    def get_nvidia_toolkit_addon_config(self, addon_name):
        """
        Get current nvidia-driver-toolkit addon configuration values

        Args:
            addon_name: Name of the addon (nvidia-driver-toolkit)

        Returns:
            dict: {'image_repo': str, 'image_tag': str, 'driver_location': str}
        """
        logging(f'Getting nvidia-driver-toolkit addon configuration for {addon_name}')
        return self.addon.get_nvidia_toolkit_configuration(addon_name)
