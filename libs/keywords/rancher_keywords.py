"""
Rancher Keywords - creates Rancher() instance and delegates - NO direct API calls!
Layer 3: Keyword wrappers for Robot Framework
"""
import os
import sys
from datetime import datetime

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))

from utility.utility import logging, generate_name_with_suffix  # noqa E402
from rancher import Rancher  # noqa E402
from constant import DEFAULT_TIMEOUT, DEFAULT_TIMEOUT_LONG, DEFAULT_RKE2_USER_DATA, DEFAULT_RKE2_NODE_CPUS, DEFAULT_RKE2_NODE_MEMORY, DEFAULT_RKE2_NODE_DISK  # noqa E402


class rancher_keywords:
    """Rancher keyword wrapper - creates Rancher component and delegates"""

    def __init__(self):
        """Initialize rancher keywords with lazy loading"""
        self._rancher = None
        self._state = {}

    @property
    def rancher(self):
        """Lazy initialize rancher to allow API client setup first"""
        if self._rancher is None:
            self._rancher = Rancher()
        return self._rancher

    def generate_unique_name(self, prefix="test"):
        """
        Generate unique name with timestamp

        Args:
            prefix: Prefix for the name

        Returns:
            str: Unique name with timestamp
        """
        timestamp = datetime.now().strftime("%Y%m%d%H%M%S")
        name = f"{prefix}-{timestamp}"
        logging(f"Generated unique name: {name}")
        return name

    # State Management
    def set_state(self, key, value):
        """Store state for later use between keywords"""
        self._state[key] = value
        logging(f"Set state {key} = {value}")

    def get_state(self, key, default=None):
        """Retrieve stored state"""
        return self._state.get(key, default)

    def clear_state(self, key=None):
        """Clear state - either specific key or all"""
        if key:
            self._state.pop(key, None)
        else:
            self._state.clear()

    # Harvester Management Cluster Operations
    def create_harvester_mgmt_cluster(self, cluster_name):
        """
        Create Harvester management cluster entry in Rancher (Import Existing)

        Args:
            cluster_name: Name of the cluster entry to create

        Returns:
            dict: Cluster data
        """
        logging(f"Creating Harvester management cluster: {cluster_name}")
        return self.rancher.create_harvester_mgmt_cluster(cluster_name)

    def get_harvester_mgmt_cluster(self, cluster_name):
        """
        Get Harvester management cluster details

        Args:
            cluster_name: Name of the cluster

        Returns:
            dict: Cluster data or None if not found
        """
        logging(f"Getting Harvester management cluster: {cluster_name}")
        return self.rancher.get_harvester_mgmt_cluster(cluster_name)

    def delete_harvester_mgmt_cluster(self, cluster_name):
        """
        Delete Harvester management cluster entry

        Args:
            cluster_name: Name of the cluster to delete
        """
        logging(f"Deleting Harvester management cluster: {cluster_name}")
        self.rancher.delete_harvester_mgmt_cluster(cluster_name)

    def wait_for_cluster_id(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for cluster to get its internal ID (status.clusterName)

        Args:
            cluster_name: Name of the cluster
            timeout: Timeout in seconds

        Returns:
            dict: Cluster data with ID
        """
        logging(f"Waiting for cluster {cluster_name} to get cluster ID")
        return self.rancher.wait_for_cluster_id(cluster_name, timeout)

    def wait_for_harvester_ready(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for Harvester cluster to be ready in Rancher

        Args:
            cluster_name: Name of the cluster
            timeout: Timeout in seconds

        Returns:
            dict: Cluster data when ready
        """
        logging(f"Waiting for Harvester cluster {cluster_name} to be ready")
        return self.rancher.wait_for_harvester_ready(cluster_name, int(timeout))

    # Import Harvester Operations
    def get_cluster_registration_url(self, cluster_id, rancher_endpoint, timeout=DEFAULT_TIMEOUT):
        """
        Get cluster registration URL for importing Harvester

        Args:
            cluster_id: Internal cluster ID (e.g. c-m-xxxxx)
            rancher_endpoint: Rancher server endpoint
            timeout: Timeout in seconds

        Returns:
            str: Registration manifest URL
        """
        logging(f"Getting cluster registration URL for: {cluster_id}")
        return self.rancher.get_cluster_registration_url(cluster_id, rancher_endpoint, timeout)

    def set_cluster_registration_url(self, url):
        """
        Set cluster-registration-url setting in Harvester

        Args:
            url: Registration URL to set
        """
        logging("Setting cluster-registration-url")
        self.rancher.set_cluster_registration_url(url)

    def get_all_rke2_versions(self, rancher_endpoint, max_versions=None):
        """
        Get all available RKE2 versions from Rancher

        Args:
            rancher_endpoint: Rancher server endpoint
            max_versions: Maximum number of versions to return (None = all)

        Returns:
            list: List of RKE2 version strings sorted newest first
        """
        logging("Getting all RKE2 versions from Rancher")
        max_ver = int(max_versions) if max_versions else None
        versions = self.rancher.get_all_rke2_versions(rancher_endpoint, max_ver)
        self.set_state('all_rke2_versions', versions)
        return versions

    def resolve_rke2_versions(self, versions_input, rancher_endpoint, max_versions=10):
        """
        Resolve RKE2 versions based on input specification.

        Handles special values:
        - "all": Get all available versions from Rancher (limited by max_versions)
        - "latest": Get only the latest version
        - "v1.28,v1.29": Get latest version matching each prefix
        - "v1.28.15+rke2r1": Use specific version as-is

        Args:
            versions_input: Version specification string
            rancher_endpoint: Rancher server endpoint
            max_versions: Max versions when using "all"

        Returns:
            list: List of resolved version strings
        """
        logging(f"Resolving RKE2 versions from input: {versions_input}")

        if not versions_input or versions_input.lower() == 'all':
            # Get all available versions
            versions = self.get_all_rke2_versions(rancher_endpoint, max_versions)
            logging(f"Resolved 'all' to {len(versions)} versions")
            return versions

        if versions_input.lower() == 'latest':
            # Get only the latest version
            versions = self.get_all_rke2_versions(rancher_endpoint, 1)
            logging(f"Resolved 'latest' to: {versions[0]}")
            return versions

        # Handle comma-separated version prefixes
        result_versions = []
        for ver_input in versions_input.split(','):
            ver_input = ver_input.strip()
            if not ver_input:
                continue

            # If it's a full version (contains +), use as-is
            if '+' in ver_input:
                result_versions.append(ver_input)
                logging(f"Using specified version: {ver_input}")
            else:
                # It's a prefix, resolve to matching version
                full_version = self.get_rke2_version(ver_input, rancher_endpoint)
                result_versions.append(full_version)

        logging(f"Resolved to {len(result_versions)} versions: {result_versions}")
        return result_versions

    def get_rke2_version(self, target_version, rancher_endpoint):
        """
        Get RKE2 version from Rancher that matches target version

        Args:
            target_version: Target version prefix (e.g. 'v1.28')
            rancher_endpoint: Rancher server endpoint

        Returns:
            str: Full RKE2 version (e.g. 'v1.28.15+rke2r1')
        """
        logging(f"Getting RKE2 version for target: {target_version}")
        version = self.rancher.get_rke2_version(target_version, rancher_endpoint)
        self.set_state('rke2_version', version)
        return version

    def configure_kdm_url(self, url):
        """
        Update the Rancher global setting rke-metadata-config to use a custom KDM URL.
        Args:
            url: URL of the custom KDM data.json file
        """
        logging(f"Configuring KDM URL: {url}")
        self.rancher.configure_kdm_url(url)
        logging("KDM URL configured successfully")

    def get_harvester_node_driver_version(self):
        """
        Get the Harvester node driver (docker-machine-driver-harvester) version

        Returns:
            str: Version string (e.g. '1.0.6')
        """
        version = self.rancher.get_harvester_node_driver_version()
        logging(f"Harvester node driver version: {version}")
        return version

    def import_harvester_to_rancher(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Full workflow to import Harvester to Rancher

        Args:
            cluster_name: Name for the Harvester entry in Rancher
            timeout: Timeout for the import operation

        Returns:
            dict: Harvester cluster data
        """
        logging(f"Importing Harvester to Rancher as: {cluster_name}")

        # Create Harvester entry in Rancher
        self.create_harvester_mgmt_cluster(cluster_name)

        # Wait for cluster to get registration URL
        cluster = self.wait_for_harvester_ready(cluster_name, timeout)
        cluster_id = cluster.get("status", {}).get("clusterName", "")

        # Get registration URL
        manifest_url = self.get_cluster_registration_url(cluster_id)

        # Set registration URL in Harvester
        self.set_cluster_registration_url(manifest_url)

        # Wait for cluster to become ready
        return self.wait_for_harvester_ready(cluster_name, timeout)

    # Cloud Credential Operations
    def create_cloud_credential(self, name, kubeconfig, cluster_id):
        """
        Create cloud credential for Harvester

        Args:
            name: Name for the credential
            kubeconfig: Kubeconfig content
            cluster_id: Harvester cluster ID

        Returns:
            dict: Credential data
        """
        logging(f"Creating cloud credential: {name}")
        return self.rancher.create_cloud_credential(name, kubeconfig, cluster_id)

    def delete_cloud_credential(self, credential_id):
        """
        Delete cloud credential

        Args:
            credential_id: ID of the credential to delete
        """
        logging(f"Deleting cloud credential: {credential_id}")
        self.rancher.delete_cloud_credential(credential_id)

    # RKE2 Cluster Operations
    def create_rke2_cluster(self, name, cloud_provider_config_id, hostname_prefix,
                            harvester_config_name, k8s_version, cloud_credential_id,
                            quantity=1, ingress="traefik"):
        """
        Create RKE2 cluster on Harvester

        Args:
            name: Cluster name
            cloud_provider_config_id: Cloud provider config secret ID
            hostname_prefix: Prefix for node hostnames
            harvester_config_name: Harvester config name
            k8s_version: Kubernetes version
            cloud_credential_id: Cloud credential ID
            quantity: Number of nodes (default: 1)
            ingress: Ingress controller to use

        Returns:
            dict: Cluster data
        """
        logging(f"Creating RKE2 cluster: {name}")
        return self.rancher.create_rke2_cluster(
            name, cloud_provider_config_id, hostname_prefix,
            harvester_config_name, k8s_version, cloud_credential_id,
            int(quantity), ingress
        )

    def get_rke2_cluster(self, cluster_name):
        """
        Get RKE2 cluster details

        Args:
            cluster_name: Name of the cluster

        Returns:
            dict: Cluster data or None if not found
        """
        logging(f"Getting RKE2 cluster: {cluster_name}")
        return self.rancher.get_rke2_cluster(cluster_name)

    def delete_rke2_cluster(self, cluster_name):
        """
        Delete RKE2 cluster

        Args:
            cluster_name: Name of the cluster to delete
        """
        logging(f"Deleting RKE2 cluster: {cluster_name}")
        self.rancher.delete_rke2_cluster(cluster_name)

    def wait_for_rke2_cluster_ready(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for RKE2 cluster to be ready

        Args:
            cluster_name: Name of the cluster
            timeout: Timeout in seconds

        Returns:
            dict: Cluster data when ready
        """
        logging(f"Waiting for RKE2 cluster {cluster_name} to be ready")
        return self.rancher.wait_for_rke2_cluster_ready(cluster_name, int(timeout))

    def wait_for_rke2_cluster_deleted(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for RKE2 cluster to be deleted

        Args:
            cluster_name: Name of the cluster
            timeout: Timeout in seconds

        Returns:
            bool: True when deleted
        """
        logging(f"Waiting for RKE2 cluster {cluster_name} to be deleted")
        return self.rancher.wait_for_rke2_cluster_deleted(cluster_name, int(timeout))

    def scale_rke2_cluster(self, cluster_name, worker_count, harvester_config_name):
        """
        Scale RKE2 cluster by adding/removing a worker-only machine pool.

        Args:
            cluster_name: Name of the RKE2 cluster
            worker_count: Number of worker nodes (0 to remove worker pool)
            harvester_config_name: HarvesterConfig name for worker pool nodes
        """
        logging(f"Scaling RKE2 cluster {cluster_name}: worker_count={worker_count}")
        self.rancher.scale_rke2_cluster(cluster_name, int(worker_count), harvester_config_name)

    def upgrade_rke2_cluster(self, cluster_name, new_k8s_version):
        """
        Upgrade RKE2 cluster to a new Kubernetes version.

        Args:
            cluster_name: Name of the RKE2 cluster
            new_k8s_version: Target Kubernetes version string
        """
        logging(f"Upgrading RKE2 cluster {cluster_name} to {new_k8s_version}")
        self.rancher.upgrade_rke2_cluster(cluster_name, new_k8s_version)

    def get_next_rke2_version(self, current_version, rancher_endpoint):
        """
        Get the latest RKE2 version in the next minor stream.

        Only minor version upgrades are supported (e.g. v1.34 -> v1.35).

        Args:
            current_version: Current RKE2 version (e.g. 'v1.34.5+rke2r1')
            rancher_endpoint: Rancher server endpoint

        Returns:
            str: Next version string, or empty string if none available
        """
        logging(f"Looking for next RKE2 minor version after {current_version}")
        from pkg_resources import parse_version

        all_versions = self.rancher.get_all_rke2_versions(rancher_endpoint)

        current_base = current_version.split("+")[0]  # e.g. v1.34.5
        parts = current_base.lstrip("v").split(".")
        if len(parts) < 2:
            logging(f"Cannot parse minor version from {current_version}")
            return ""

        current_minor = int(parts[1])
        next_minor_prefix = f"v{parts[0]}.{current_minor + 1}."

        candidates = []
        for v in all_versions:
            v_base = v.split("+")[0]
            if v_base.startswith(next_minor_prefix):
                candidates.append((parse_version(v_base), v))

        if not candidates:
            logging(f"No version found in next minor stream ({next_minor_prefix}) "
                    f"for {current_version}")
            return ""

        # Return the latest patch in the next minor stream
        candidates.sort(key=lambda x: x[0], reverse=True)
        next_version = candidates[0][1]
        logging(f"Found next RKE2 version: {next_version}")
        return next_version

    def verify_lb_response(self, url, expected_text=None):
        """
        Verify LoadBalancer is responding with expected content.

        Args:
            url: URL to query
            expected_text: Text expected in response (optional, any 200 OK passes if None)

        Returns:
            bool: True if response matches
        """
        logging(f"Verifying LB response from: {url}")
        code, text = self.rancher.query_lb_service(url)
        if code == 200:
            if expected_text and expected_text not in text:
                raise Exception(f"Expected '{expected_text}' in response, got: {text[:200]}")
            logging("LB response verified successfully")
            return True
        raise Exception(f"Expected 200 OK, got: {code}, {text[:200]}")

    def verify_lb_via_cluster(self, cluster_id, service_name, port=8080,
                              namespace="default", expected_text=None):
        """
        Verify LoadBalancer service via Rancher's Kubernetes service proxy.

        Uses /k8s/clusters/{id}/api/v1/namespaces/{ns}/services/{svc}:{port}/proxy/
        which works regardless of whether the LB VIP is routable from the test runner.

        Args:
            cluster_id: Downstream cluster ID (e.g. c-m-xxxxx)
            service_name: Name of the LoadBalancer service
            port: Service port (default 8080)
            namespace: Service namespace (default: default)
            expected_text: Text expected in response (optional)

        Returns:
            bool: True if response matches
        """
        logging(f"Verifying LB {service_name} via Rancher proxy (cluster {cluster_id})")
        code, text = self.rancher.query_lb_via_proxy(
            cluster_id, service_name, port, namespace)
        if code == 200:
            if expected_text and expected_text not in str(text):
                raise Exception(
                    f"Expected '{expected_text}' in response, got: {str(text)[:200]}")
            logging("LB response verified successfully via proxy")
            return True
        raise Exception(f"Expected 200 OK via proxy, got: {code}, {str(text)[:200]}")

    # Harvester Config Operations
    def create_harvester_config(self, name, cpus=None, mems=None, disks=None, image_id="",
                                network_id="", ssh_user="ubuntu", user_data=None):
        """
        Create Harvester config for RKE2 node template

        Args:
            name: Config name
            cpus: Number of CPUs (default: DEFAULT_RKE2_NODE_CPUS)
            mems: Memory in GB (default: DEFAULT_RKE2_NODE_MEMORY)
            disks: Disk size in GB (default: DEFAULT_RKE2_NODE_DISK)
            image_id: Image ID to use
            network_id: Network name
            ssh_user: SSH user (default: ubuntu)
            user_data: Cloud-init user data

        Returns:
            dict: Config data
        """
        if not cpus:
            cpus = DEFAULT_RKE2_NODE_CPUS
        if not mems:
            mems = DEFAULT_RKE2_NODE_MEMORY
        if not disks:
            disks = DEFAULT_RKE2_NODE_DISK
        if not user_data:
            user_data = DEFAULT_RKE2_USER_DATA
        logging(f"Creating Harvester config: {name}")
        return self.rancher.create_harvester_config(
            name, int(cpus), int(mems), int(disks),
            image_id, network_id, ssh_user, user_data
        )

    # Kubeconfig Operations
    def generate_kubeconfig(self, cluster_id, cluster_name):
        """
        Generate full-access kubeconfig for a cluster (for cloud credentials)

        Args:
            cluster_id: Cluster ID
            cluster_name: Cluster name

        Returns:
            str: Kubeconfig content
        """
        logging(f"Generating kubeconfig for cluster: {cluster_name}")
        return self.rancher.generate_kubeconfig(cluster_id, cluster_name)

    def generate_cloud_provider_kubeconfig(self, cluster_id, cluster_name):
        """
        Generate cloud provider kubeconfig with external URL (for cloud provider secret)

        Args:
            cluster_id: Cluster ID
            cluster_name: Cluster name

        Returns:
            str: Kubeconfig content with external Rancher URL
        """
        logging(f"Generating cloud provider kubeconfig for cluster: {cluster_name}")
        return self.rancher.generate_cloud_provider_kubeconfig(cluster_id, cluster_name)

    # Secret Operations
    def create_cloud_provider_secret(self, name, kubeconfig, cluster_name):
        """
        Create secret for cloud provider config

        Args:
            name: Secret name
            kubeconfig: Kubeconfig content
            cluster_name: Cluster name for annotations

        Returns:
            dict: Secret data
        """
        logging(f"Creating cloud provider secret: {name}")
        annotations = {
            "v2prov-secret-authorized-for-cluster": cluster_name,
            "v2prov-authorized-secret-deletes-on-cluster-removal": "true"
        }
        data = {"credential": kubeconfig.replace("\\n", "\n")}
        return self.rancher.create_secret(name, data, annotations)

    # Deployment Operations
    def create_deployment(self, cluster_id, namespace, name, image, pvc=None):
        """
        Create deployment in guest cluster

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Deployment name
            image: Container image
            pvc: PVC name to mount (optional)

        Returns:
            dict: Deployment data
        """
        logging(f"Creating deployment {name} in cluster {cluster_id}")
        return self.rancher.create_deployment(cluster_id, namespace, name, image, pvc)

    def delete_deployment(self, cluster_id, namespace, name):
        """
        Delete deployment from guest cluster

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Deployment name
        """
        logging(f"Deleting deployment {name} from cluster {cluster_id}")
        self.rancher.delete_deployment(cluster_id, namespace, name)

    def wait_for_deployment_ready(self, cluster_id, namespace, name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for deployment to be ready

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Deployment name
            timeout: Timeout in seconds

        Returns:
            dict: Deployment data when ready
        """
        logging(f"Waiting for deployment {name} to be ready")
        return self.rancher.wait_for_deployment_ready(
            cluster_id, namespace, name, int(timeout)
        )

    def wait_for_deployment_deleted(self, cluster_id, namespace, name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for deployment to be deleted

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Deployment name
            timeout: Timeout in seconds

        Returns:
            bool: True when deleted
        """
        logging(f"Waiting for deployment {name} to be deleted")
        return self.rancher.wait_for_deployment_deleted(
            cluster_id, namespace, name, int(timeout)
        )

    def scale_deployment(self, cluster_id, namespace, name, replicas):
        """
        Scale a deployment in a guest cluster to the given replica count

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Deployment name
            replicas: Desired replica count
        """
        logging(f"Scaling deployment {name} to {replicas} replicas")
        return self.rancher.scale_deployment(
            cluster_id, namespace, name, int(replicas)
        )

    def wait_for_deployment_scaled(self, cluster_id, namespace, name,
                                   replicas, timeout=DEFAULT_TIMEOUT):
        """
        Wait for a deployment to reach the given ready replica count

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Deployment name
            replicas: Expected ready replica count
            timeout: Timeout in seconds

        Returns:
            dict: Deployment data when scaled
        """
        logging(f"Waiting for deployment {name} to scale to {replicas}")
        return self.rancher.wait_for_deployment_scaled(
            cluster_id, namespace, name, int(replicas), int(timeout)
        )

    # PVC Operations
    def create_pvc(self, cluster_id, name, size="1Gi", storage_class=None):
        """
        Create PVC in guest cluster

        Args:
            cluster_id: Cluster ID
            name: PVC name
            size: Storage size (default: 1Gi)
            storage_class: Storage class name (optional)

        Returns:
            dict: PVC data
        """
        logging(f"Creating PVC {name} in cluster {cluster_id}")
        return self.rancher.create_pvc(cluster_id, name, size, storage_class)

    def delete_pvc(self, cluster_id, name):
        """
        Delete PVC from guest cluster

        Args:
            cluster_id: Cluster ID
            name: PVC name
        """
        logging(f"Deleting PVC {name} from cluster {cluster_id}")
        self.rancher.delete_pvc(cluster_id, name)

    def wait_for_pvc_bound(self, cluster_id, name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for PVC to be bound

        Args:
            cluster_id: Cluster ID
            name: PVC name
            timeout: Timeout in seconds

        Returns:
            dict: PVC data when bound
        """
        logging(f"Waiting for PVC {name} to be bound")
        return self.rancher.wait_for_pvc_bound(cluster_id, name, int(timeout))

    # Load Balancer Service Operations
    def create_lb_service(self, cluster_id, namespace, name, target_deployment,
                          port=8080, target_port=80, ipam="dhcp"):
        """
        Create LoadBalancer service

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: Service name
            target_deployment: Deployment to target
            port: Service port (default: 8080)
            target_port: Target port (default: 80)
            ipam: IPAM mode - 'dhcp' or 'pool' (default: dhcp)

        Returns:
            dict: Service data
        """
        logging(f"Creating LoadBalancer service {name} in cluster {cluster_id}")

        service_data = {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": {
                "namespace": namespace,
                "name": name,
                "annotations": {
                    "cloudprovider.harvesterhci.io/ipam": ipam
                }
            },
            "spec": {
                "type": "LoadBalancer",
                "sessionAffinity": "None",
                "ports": [
                    {
                        "name": "http",
                        "port": int(port),
                        "protocol": "TCP",
                        "targetPort": int(target_port)
                    }
                ],
                "selector": {
                    "name": target_deployment
                }
            }
        }

        return self.rancher.create_lb_service(cluster_id, service_data)

    def delete_lb_service(self, cluster_id, name):
        """
        Delete LoadBalancer service

        Args:
            cluster_id: Cluster ID
            name: Service name
        """
        logging(f"Deleting LoadBalancer service {name} from cluster {cluster_id}")
        self.rancher.delete_lb_service(cluster_id, name)

    def wait_for_lb_service_ready(self, cluster_id, name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for LoadBalancer service to be ready

        Args:
            cluster_id: Cluster ID
            name: Service name
            timeout: Timeout in seconds

        Returns:
            dict: Service data when ready
        """
        logging(f"Waiting for LoadBalancer service {name} to be ready")
        return self.rancher.wait_for_lb_service_ready(cluster_id, name, int(timeout))

    def query_lb_service(self, url):
        """
        Query LoadBalancer service endpoint

        Args:
            url: URL to query

        Returns:
            tuple: (status_code, response_text)
        """
        logging(f"Querying LoadBalancer service at: {url}")
        return self.rancher.query_lb_service(url)

    def verify_lb_nginx_response(self, url):
        """
        Verify LoadBalancer returns nginx welcome page

        Args:
            url: URL to query

        Returns:
            bool: True if nginx welcome page found
        """
        logging(f"Verifying nginx response from: {url}")
        code, text = self.rancher.query_lb_service(url)
        if code == 200 and "Welcome to nginx" in text:
            logging("Nginx welcome page verified")
            return True
        raise Exception(f"Expected nginx welcome page, got: {code}, {text}")

    # Harvester Deployments Check
    def wait_for_harvester_deployments_ready(self, cluster_id, timeout=DEFAULT_TIMEOUT):
        """
        Wait for harvester-cloud-provider and harvester-csi-driver to be ready

        Args:
            cluster_id: Cluster ID
            timeout: Timeout in seconds
        """
        logging(f"Waiting for Harvester deployments in cluster {cluster_id}")
        self.rancher.wait_for_harvester_deployments_ready(cluster_id, int(timeout))

    # Import Existing Cluster Operations
    def create_import_cluster(self, name):
        """
        Create a minimal provisioning cluster for importing an existing cluster.

        Args:
            name: Cluster name

        Returns:
            dict: Cluster data
        """
        logging(f"Creating import cluster: {name}")
        return self.rancher.create_import_cluster(name)

    def wait_for_import_cluster_ready(self, cluster_name,
                                      timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for an imported cluster to become active in Rancher.

        Args:
            cluster_name: Name of the provisioning cluster
            timeout: Timeout in seconds

        Returns:
            dict: Cluster data when ready
        """
        logging(f"Waiting for import cluster {cluster_name} to be ready")
        return self.rancher.wait_for_import_cluster_ready(
            cluster_name, int(timeout)
        )

    # Custom RKE2 Cluster Operations
    def create_custom_rke2_cluster(self, name, cloud_provider_config_id,
                                   k8s_version, cloud_credential_id,
                                   ingress="traefik"):
        """
        Create a custom RKE2 cluster without machinePools.

        Nodes are registered externally via the registration command.
        The Harvester cloud provider is configured.

        Args:
            name: Cluster name
            cloud_provider_config_id: Cloud provider config secret ID
            k8s_version: Kubernetes version
            cloud_credential_id: Cloud credential ID
            ingress: Ingress controller (default: traefik)

        Returns:
            dict: Cluster data
        """
        logging(f"Creating custom RKE2 cluster: {name}")
        return self.rancher.create_custom_rke2_cluster(
            name, cloud_provider_config_id, k8s_version,
            cloud_credential_id, ingress
        )

    def update_cluster_chart_name(self, cluster_name, mgmt_cluster_id):
        """
        Patch a custom cluster's cloud provider clusterName with the
        real management cluster ID so the cloud provider can match VMs.

        Args:
            cluster_name: Provisioning cluster name
            mgmt_cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(f"Updating chart clusterName to {mgmt_cluster_id}")
        return self.rancher.update_cluster_chart_name(
            cluster_name, mgmt_cluster_id
        )

    def fix_cloud_provider_cluster_name(self, cluster_id):
        """
        Fix the cloud-provider --cluster-name arg on the guest cluster.

        Rancher deploys the cloud-provider chart with the provisioning
        name, but the cloud provider needs the management cluster ID
        to match VMs. This patches the deployment and waits for rollout.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(f"Fixing cloud provider clusterName to {cluster_id}")
        return self.rancher.fix_cloud_provider_cluster_name(cluster_id)

    def get_cluster_registration_command(self, cluster_name,
                                         timeout=DEFAULT_TIMEOUT_LONG):
        """
        Get the insecure node registration command for a custom cluster.

        Args:
            cluster_name: Name of the provisioning cluster
            timeout: Timeout in seconds

        Returns:
            str: The insecure node command to run on VMs
        """
        logging(f"Getting registration command for: {cluster_name}")
        return self.rancher.get_cluster_registration_command(
            cluster_name, int(timeout)
        )

    # Harvester VM Operations (for custom cluster nodes)
    def create_harvester_vm(self, name, image_id, network_id, cpus=2,
                            memory=4, disk_size=40, ssh_user="ubuntu",
                            user_data="", network_data="",
                            guest_cluster_id=""):
        """
        Create a VM on Harvester.

        Args:
            name: VM name
            image_id: Image ID (namespace/name format)
            network_id: Network ID (namespace/name format)
            cpus: Number of CPU cores (default: 2)
            memory: Memory in GiB (default: 4)
            disk_size: Disk size in GiB (default: 40)
            ssh_user: SSH username (default: ubuntu)
            user_data: Cloud-init user data string
            network_data: Cloud-init network data string (NoCloud network config v2)
            guest_cluster_id: Management cluster ID (c-m-xxxxx). When set
                adds LB-required labels to the VM.

        Returns:
            dict: VM data
        """
        logging(f"Creating Harvester VM: {name}")
        return self.rancher.create_harvester_vm(
            name, image_id, network_id, int(cpus), int(memory),
            int(disk_size), ssh_user, user_data, network_data,
            guest_cluster_id
        )

    # Chart Install Keywords
    def install_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """
        Install a Helm chart on a guest cluster via Rancher catalog API.

        Args:
            cluster_id: Rancher management cluster ID
            repo_name: Chart repository name
            chart_name: Chart name
            version: Chart version (empty string for latest)
            release_name: Helm release name
            namespace: Target namespace
            values: Optional dict of chart values overrides
        """
        if not version:
            versions = self.rancher.get_chart_versions(
                repo_name, chart_name, cluster_id
            )
            if not versions:
                raise Exception(
                    f"No versions found for chart {chart_name} "
                    f"in {repo_name}"
                )
            version = versions[0]
            logging(f"Using latest chart version: {version}")

        logging(f"Installing chart {chart_name} v{version} as "
                f"{release_name} in {namespace}")
        return self.rancher.install_chart(
            cluster_id, repo_name, chart_name, version,
            release_name, namespace, values
        )

    def upgrade_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """
        Upgrade an installed Helm chart on a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            repo_name: Chart repository name
            chart_name: Chart name
            version: Target chart version (empty string for latest)
            release_name: Helm release name
            namespace: Target namespace
            values: Optional dict of chart values overrides
        """
        if not version:
            versions = self.rancher.get_chart_versions(
                repo_name, chart_name, cluster_id
            )
            if not versions:
                raise Exception(
                    f"No versions found for chart {chart_name} "
                    f"in {repo_name}"
                )
            version = versions[0]
            logging(f"Using latest chart version: {version}")

        logging(f"Upgrading chart {chart_name} to v{version} as "
                f"{release_name} in {namespace}")
        return self.rancher.upgrade_chart(
            cluster_id, repo_name, chart_name, version,
            release_name, namespace, values
        )

    def uninstall_chart(self, cluster_id, release_name, namespace):
        """
        Uninstall a Helm chart (app) from a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name
            namespace: Namespace where the chart is installed
        """
        logging(f"Uninstalling chart {release_name} from {namespace}")
        return self.rancher.uninstall_chart(
            cluster_id, release_name, namespace
        )

    def wait_for_chart_app_deleted(self, cluster_id, release_name, namespace,
                                   timeout=DEFAULT_TIMEOUT):
        """
        Wait for a chart app to be fully removed from a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name
            namespace: Namespace where the chart was installed
            timeout: Timeout in seconds

        Returns:
            bool: True when deleted
        """
        logging(f"Waiting for chart app {release_name} to be deleted")
        return self.rancher.wait_for_chart_app_deleted(
            cluster_id, release_name, namespace, int(timeout)
        )

    def get_chart_versions(self, repo_name, chart_name, cluster_id=None):
        """
        Get available versions for a chart from a Rancher chart repository.

        Args:
            repo_name: Chart repository name
            chart_name: Chart name
            cluster_id: Optional guest cluster ID to query via proxy

        Returns:
            list: Available versions sorted newest first
        """
        logging(f"Getting versions for chart {chart_name} from {repo_name}")
        return self.rancher.get_chart_versions(
            repo_name, chart_name, cluster_id
        )

    def get_deployed_chart_version(self, cluster_id, release_name, namespace):
        """
        Return the deployed version string of an installed chart app.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name (e.g. harvester-csi-driver)
            namespace: Namespace where the chart was installed

        Returns:
            str: Deployed chart version (e.g. '0.1.18')
        """
        return self.rancher.get_deployed_chart_version(
            cluster_id, release_name, namespace
        )

    def create_cluster_repo(self, cluster_id, repo_name, git_url, git_branch):
        """
        Create a ClusterRepo on a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            repo_name: Name for the ClusterRepo resource
            git_url: Git repository URL
            git_branch: Git branch name
        """
        logging(f"Creating ClusterRepo {repo_name} (branch={git_branch})")
        return self.rancher.create_cluster_repo(
            cluster_id, repo_name, git_url, git_branch
        )

    def wait_for_cluster_repo_ready(self, cluster_id, repo_name,
                                    timeout=DEFAULT_TIMEOUT,
                                    expected_git_branch=None):
        """
        Wait for a ClusterRepo to finish downloading on a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            repo_name: ClusterRepo name
            timeout: Maximum wait time in seconds
            expected_git_branch: When set, verify spec.gitBranch matches and
                                 re-apply if Rancher has reconciled it back
        """
        logging(f"Waiting for ClusterRepo {repo_name} to be ready")
        return self.rancher.wait_for_cluster_repo_ready(
            cluster_id, repo_name, timeout, expected_git_branch
        )

    def create_cloud_config_secret(self, cluster_id, secret_name,
                                   namespace, kubeconfig):
        """
        Create a cloud-provider-config secret on a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            secret_name: Name for the secret
            namespace: Target namespace
            kubeconfig: Harvester cloud-provider kubeconfig content
        """
        logging(f"Creating cloud config secret {secret_name}")
        return self.rancher.create_cloud_config_secret(
            cluster_id, secret_name, namespace, kubeconfig
        )

    def write_cloud_config_to_nodes(self, cluster_id, secret_name, namespace):
        """
        Write cloud-provider-config from secret to each node's hostPath
        via a temporary privileged DaemonSet on the guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            secret_name: Name of the secret containing 'cloud-provider-config'
            namespace: Namespace of the secret
        """
        logging(f"Writing cloud config to nodes on cluster {cluster_id}")
        return self.rancher.write_cloud_config_to_nodes(
            cluster_id, secret_name, namespace
        )

    def wait_for_chart_app_ready(self, cluster_id, release_name,
                                 namespace, timeout=DEFAULT_TIMEOUT,
                                 expected_version=None):
        """
        Wait for a chart app to be deployed and ready.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name
            namespace: Namespace where chart was installed
            timeout: Timeout in seconds
            expected_version: If set, also wait for this exact chart version
        """
        logging(f"Waiting for chart app {release_name} to be ready")
        return self.rancher.wait_for_chart_app_ready(
            cluster_id, release_name, namespace, int(timeout), expected_version
        )

    # RWX Volume / StorageClass / StatefulSet Operations
    def create_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """
        Create an RWX-capable StorageClass on the host Harvester cluster.

        Uses Longhorn provisioner with NFS options for ReadWriteMany support.

        Args:
            name: StorageClass name (default: longhorn-rwx)

        Returns:
            dict: StorageClass data
        """
        logging(f"Creating RWX StorageClass on host: {name}")
        return self.rancher.create_rwx_storage_class_on_host(name)

    def delete_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """
        Delete the RWX StorageClass from the host Harvester cluster.

        Args:
            name: StorageClass name (default: longhorn-rwx)
        """
        logging(f"Deleting RWX StorageClass from host: {name}")
        self.rancher.delete_rwx_storage_class_on_host(name)

    def create_guest_storage_class(self, cluster_id, name,
                                   host_storage_class="longhorn-rwx"):
        """
        Create a StorageClass on the guest cluster referencing a host StorageClass.

        Uses the Harvester CSI driver provisioner with hostStorageClass parameter.

        Args:
            cluster_id: Guest cluster ID
            name: StorageClass name on the guest cluster
            host_storage_class: Name of the host-side StorageClass to reference

        Returns:
            dict: StorageClass data
        """
        logging(f"Creating guest StorageClass {name} in cluster {cluster_id}")
        return self.rancher.create_guest_storage_class(
            cluster_id, name, host_storage_class
        )

    def delete_guest_storage_class(self, cluster_id, name):
        """
        Delete a StorageClass from the guest cluster.

        Args:
            cluster_id: Guest cluster ID
            name: StorageClass name
        """
        logging(f"Deleting guest StorageClass {name} from cluster {cluster_id}")
        self.rancher.delete_guest_storage_class(cluster_id, name)

    def create_pvc_rwx(self, cluster_id, name, size="1Gi", storage_class=None):
        """
        Create a ReadWriteMany PVC in guest cluster

        Args:
            cluster_id: Cluster ID
            name: PVC name
            size: Storage size (default: 1Gi)
            storage_class: Storage class name (optional)

        Returns:
            dict: PVC data
        """
        logging(f"Creating RWX PVC {name} in cluster {cluster_id}")
        return self.rancher.create_pvc_rwx(cluster_id, name, size, storage_class)

    def create_statefulset(self, cluster_id, namespace, name, image,
                           pvc_name, replicas=2):
        """
        Create a StatefulSet that mounts an existing PVC

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: StatefulSet name
            image: Container image
            pvc_name: Existing PVC name to mount
            replicas: Number of replicas (default: 2)

        Returns:
            dict: StatefulSet data
        """
        logging(f"Creating StatefulSet {name} in cluster {cluster_id}")
        return self.rancher.create_statefulset(
            cluster_id, namespace, name, image, pvc_name,
            int(replicas)
        )

    def delete_statefulset(self, cluster_id, namespace, name):
        """
        Delete StatefulSet from guest cluster

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: StatefulSet name
        """
        logging(f"Deleting StatefulSet {name} from cluster {cluster_id}")
        self.rancher.delete_statefulset(cluster_id, namespace, name)

    def wait_for_statefulset_ready(self, cluster_id, namespace, name,
                                   timeout=DEFAULT_TIMEOUT):
        """
        Wait for StatefulSet to have all replicas ready

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: StatefulSet name
            timeout: Timeout in seconds

        Returns:
            dict: StatefulSet data when ready
        """
        logging(f"Waiting for StatefulSet {name} to be ready")
        return self.rancher.wait_for_statefulset_ready(
            cluster_id, namespace, name, int(timeout)
        )

    def exec_command_in_pod(self, cluster_id, namespace, pod_name, command):
        """
        Execute a command inside a pod

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            pod_name: Pod name
            command: Command list to execute

        Returns:
            str: Command stdout
        """
        logging(f"Executing command in pod {pod_name}")
        return self.rancher.exec_command_in_pod(
            cluster_id, namespace, pod_name, list(command)
        )

    def get_pods_by_label(self, cluster_id, namespace, label_selector):
        """
        Get pods matching a label selector

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            label_selector: Label selector string (e.g. 'name=myapp')

        Returns:
            list: List of pod dicts
        """
        logging(f"Getting pods with label {label_selector}")
        return self.rancher.get_pods_by_label(
            cluster_id, namespace, label_selector
        )

    # Rancher RBAC Operations

    def create_rancher_user(self, user_id, display_name):
        """
        Create a new Rancher local user.

        Args:
            user_id: Username (metadata.name and username field)
            display_name: Human-readable display name

        Returns:
            dict: Created user resource
        """
        logging(f"Creating Rancher user: {user_id}")
        return self.rancher.create_rancher_user(user_id, display_name)

    def delete_rancher_user(self, user_id):
        """
        Delete a Rancher user and its associated password secret.

        Args:
            user_id: Username to delete
        """
        logging(f"Deleting Rancher user: {user_id}")
        self.rancher.delete_rancher_user(user_id)

    def set_user_password(self, user_id, password):
        """
        Set or update the password for a Rancher local user.

        Args:
            user_id: Username whose password to set
            password: Plaintext password value
        """
        logging(f"Setting password for Rancher user: {user_id}")
        self.rancher.set_user_password(user_id, password)

    def assign_standard_user_role(self, user_id):
        """
        Assign the Standard User global role to a Rancher user.

        Creates a GlobalRoleBinding with globalRoleName: user.

        Args:
            user_id: Username to assign the role to
        """
        logging(f"Assigning Standard User role to: {user_id}")
        self.rancher.assign_standard_user_role(user_id)

    def create_project(self, cluster_id, display_name):
        """
        Create a new Rancher project and return its short project ID.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            display_name: Display name for the project

        Returns:
            str: Short project ID (e.g. p-xxxxx)
        """
        logging(f"Creating project '{display_name}' in cluster {cluster_id}")
        return self.rancher.create_project(cluster_id, display_name)

    def delete_project(self, cluster_id, project_id):
        """
        Delete a Rancher project.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        logging(f"Deleting project {project_id} in cluster {cluster_id}")
        self.rancher.delete_project(cluster_id, project_id)

    def assign_project_role(self, user_id, cluster_id, project_id, role_template_name):
        """
        Assign a project-scoped role to a Rancher user via ProjectRoleTemplateBinding.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-project-view)
        """
        logging(
            f"Assigning project role '{role_template_name}' to "
            f"user '{user_id}' in project {cluster_id}:{project_id}"
        )
        self.rancher.assign_project_role(
            user_id, cluster_id, project_id, role_template_name
        )

    def delete_user_global_role_bindings(self, user_id):
        """
        Delete all GlobalRoleBindings owned by the given user.

        Args:
            user_id: Username whose GlobalRoleBindings to remove
        """
        logging(f"Deleting GlobalRoleBindings for user: {user_id}")
        self.rancher.delete_user_global_role_bindings(user_id)

    def delete_user_project_role_bindings(self, user_id, cluster_id, project_id):
        """
        Delete all ProjectRoleTemplateBindings owned by the user in a project.

        Args:
            user_id: Username whose bindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        logging(
            f"Deleting project role bindings for user '{user_id}' "
            f"in project {cluster_id}:{project_id}"
        )
        self.rancher.delete_user_project_role_bindings(user_id, cluster_id, project_id)

    def assign_cluster_role(self, user_id, cluster_id, role_template_name):
        """
        Assign a cluster-scoped role to a Rancher user via ClusterRoleTemplateBinding.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-cluster-view)
        """
        logging(
            f"Assigning cluster role '{role_template_name}' to "
            f"user '{user_id}' in cluster {cluster_id}"
        )
        self.rancher.assign_cluster_role(user_id, cluster_id, role_template_name)

    def delete_user_cluster_role_bindings(self, user_id, cluster_id):
        """
        Delete all ClusterRoleTemplateBindings owned by the user in a cluster.

        Args:
            user_id: Username whose ClusterRoleTemplateBindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(
            f"Deleting cluster role bindings for user '{user_id}' "
            f"in cluster {cluster_id}"
        )
        self.rancher.delete_user_cluster_role_bindings(user_id, cluster_id)

    def generate_user_kubeconfig(self, user_id, password, cluster_id):
        """
        Login as user_id and return a kubeconfig YAML string for cluster_id.

        Args:
            user_id: Rancher username
            password: User's login password
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)

        Returns:
            str: kubeconfig YAML content ready for kubectl
        """
        logging(f"Generating kubeconfig for user '{user_id}'")
        return self.rancher.generate_user_kubeconfig(user_id, password, cluster_id)

    def verify_resource_access(self, kubeconfig_content, verb, resource, namespace):
        """
        Run 'kubectl auth can-i <verb> <resource> -n <namespace>' with the given kubeconfig.

        Args:
            kubeconfig_content: kubeconfig YAML string
            verb: kubectl verb (e.g. 'get', 'create')
            resource: Resource type (e.g. 'virtualmachines.kubevirt.io')
            namespace: Target namespace

        Returns:
            tuple: (True, output) if allowed, (False, output) if denied
        """
        logging(f"Verifying '{verb}' access to '{resource}' in namespace '{namespace}'")
        return self.rancher.verify_resource_access(
            kubeconfig_content, verb, resource, namespace
        )

    def create_namespace_in_project(self, namespace_name, cluster_id, project_id):
        """
        Create a namespace in the Harvester cluster and bind it to a Rancher project.

        Args:
            namespace_name: Name for the new namespace
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        logging(f"Creating namespace '{namespace_name}' in project {cluster_id}:{project_id}")
        return self.rancher.create_namespace_in_project(
            namespace_name, cluster_id, project_id
        )

    def delete_namespace_from_cluster(self, namespace_name, cluster_id):
        """
        Delete a namespace from the Harvester cluster.

        Args:
            namespace_name: Namespace to delete
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(f"Deleting namespace '{namespace_name}'")
        self.rancher.delete_namespace_from_cluster(namespace_name, cluster_id)
