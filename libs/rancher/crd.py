"""
Rancher CRD Implementation - Kubernetes API operations
Layer 4: Makes actual kubectl/K8s API calls for Rancher integration operations

This implementation uses kubectl commands against Rancher's Kubernetes API
for managing Rancher resources (clusters, etc.) via CRD approach.
"""

import os
import time
import subprocess
import json
import base64
import tempfile
import re
import requests
import yaml
from urllib.parse import urlsplit, urlunsplit
from kubernetes import client
from kubernetes.client.rest import ApiException
from constant import (
    DEFAULT_TIMEOUT,
    DEFAULT_TIMEOUT_LONG,
    HARVESTER_API_GROUP,
    HARVESTER_API_VERSION,
    DEFAULT_NAMESPACE,
)
from utility.utility import logging, get_retry_count_and_interval
from rancher.base import Base


class CRD(Base):
    """
    CRD implementation for Rancher Integration operations using Kubernetes API

    This implementation uses kubectl against Rancher's Kubernetes API for
    managing Rancher resources (clusters, ClusterRegistrationTokens, etc.).

    For Harvester-local operations, it uses the default kubeconfig (Harvester).
    For Rancher operations, it generates a kubeconfig targeting Rancher's K8s API.
    """

    def __init__(self):
        """Initialize Kubernetes clients"""
        # Harvester K8s clients (for Harvester-local operations)
        self.core_api = client.CoreV1Api()
        self.custom_api = client.CustomObjectsApi()
        self.apps_api = client.AppsV1Api()

        # Harvester API groups
        self.harvester_group = HARVESTER_API_GROUP
        self.harvester_version = HARVESTER_API_VERSION

        # Rancher API groups (for kubectl commands against Rancher)
        self.rancher_mgmt_group = "management.cattle.io"
        self.rancher_mgmt_version = "v3"
        self.rancher_provisioning_group = "provisioning.cattle.io"
        self.rancher_provisioning_version = "v1"
        self.rancher_machine_config_group = "rke-machine-config.cattle.io"
        self.rancher_machine_config_version = "v1"

        # Rancher authentication
        self.rancher_token = None
        self.rancher_endpoint = None
        self._rancher_kubeconfig_path = None
        self._guest_kubeconfig_paths = {}

    def __del__(self):
        """Cleanup temporary kubeconfig files"""
        for path in [self._rancher_kubeconfig_path,
                     *self._guest_kubeconfig_paths.values()]:
            if path and os.path.exists(path):
                try:
                    os.remove(path)
                except OSError:
                    pass

    def _get_rancher_credentials(self):
        """Get Rancher credentials from Robot variables or environment"""
        try:
            from robot.libraries.BuiltIn import BuiltIn
            endpoint = BuiltIn().get_variable_value("${RANCHER_ENDPOINT}", "")
            api_key = BuiltIn().get_variable_value("${RANCHER_API_KEY}", "")
            username = BuiltIn().get_variable_value("${RANCHER_USERNAME}", "admin")
            password = BuiltIn().get_variable_value("${RANCHER_PASSWORD}", "password1234")
        except Exception:
            endpoint = os.getenv("RANCHER_ENDPOINT", "")
            api_key = os.getenv("RANCHER_API_KEY", "")
            username = os.getenv("RANCHER_USERNAME", "admin")
            password = os.getenv("RANCHER_PASSWORD", "password1234")

        if not endpoint:
            raise Exception("RANCHER_ENDPOINT not configured")

        parsed_endpoint = urlsplit(endpoint)
        if parsed_endpoint.scheme == "http":
            endpoint = urlunsplit(("https", parsed_endpoint.netloc,
                                   parsed_endpoint.path, parsed_endpoint.query,
                                   parsed_endpoint.fragment))

        return endpoint, api_key, username, password

    def _authenticate_rancher(self):
        """Authenticate with Rancher and get API token"""
        if self.rancher_token:
            return self.rancher_token

        endpoint, api_key, username, password = self._get_rancher_credentials()
        self.rancher_endpoint = endpoint

        session = requests.Session()
        session.verify = False
        session.headers.update({"Content-Type": "application/json"})

        if api_key:
            token = api_key.removeprefix("Bearer ").strip()
            self.rancher_token = token
            logging(f"Authenticating with Rancher at {endpoint} using API key")
            return token

        logging(f"Authenticating with Rancher at {endpoint} as user {username}")

        from urllib.parse import urljoin
        url = urljoin(endpoint, "v3-public/localProviders/local?action=login")

        response = session.post(url, json={"username": username, "password": password})

        if response.status_code != 201:
            logging(f"Auth failed - Status: {response.status_code}, Response: {response.text}")
            raise Exception(
                f"Failed to authenticate with Rancher: "
                f"{response.status_code} {response.text}"
            )

        token = response.json().get("token")
        if not token:
            raise Exception("No token in authentication response")

        self.rancher_token = token
        logging("Successfully authenticated with Rancher")
        return token

    def _get_rancher_kubeconfig(self):
        """
        Generate a kubeconfig file for accessing Rancher's Kubernetes API.

        Rancher exposes its K8s API at /k8s/clusters/local for the management cluster.
        We use the Rancher API token as bearer token for authentication.

        Returns:
            str: Path to temporary kubeconfig file
        """
        if self._rancher_kubeconfig_path and os.path.exists(self._rancher_kubeconfig_path):
            return self._rancher_kubeconfig_path

        # Ensure we have auth token
        token = self._authenticate_rancher()

        # Rancher's K8s API endpoint for the local/management cluster
        # This is where provisioning.cattle.io CRDs are managed
        k8s_endpoint = f"{self.rancher_endpoint.rstrip('/')}/k8s/clusters/local"

        # Generate kubeconfig content
        kubeconfig = {
            "apiVersion": "v1",
            "kind": "Config",
            "clusters": [{
                "name": "rancher-local",
                "cluster": {
                    "server": k8s_endpoint,
                    "insecure-skip-tls-verify": True
                }
            }],
            "contexts": [{
                "name": "rancher-local",
                "context": {
                    "cluster": "rancher-local",
                    "user": "rancher-admin"
                }
            }],
            "current-context": "rancher-local",
            "users": [{
                "name": "rancher-admin",
                "user": {
                    "token": token
                }
            }]
        }

        # Write to temporary file
        fd, path = tempfile.mkstemp(suffix=".kubeconfig", prefix="rancher-")
        with os.fdopen(fd, 'w') as f:
            yaml.dump(kubeconfig, f)

        self._rancher_kubeconfig_path = path
        logging(f"Created Rancher kubeconfig at: {path}")
        return path

    def _run_kubectl_rancher(self, args, input_data=None):
        """
        Run kubectl command against Rancher's Kubernetes API.

        This uses a dynamically generated kubeconfig that targets Rancher's
        K8s API at /k8s/clusters/local with the Rancher API token.

        Args:
            args: List of kubectl arguments
            input_data: Optional stdin data to pipe to kubectl

        Returns:
            tuple: (return_code, stdout, stderr)
        """
        kubeconfig = self._get_rancher_kubeconfig()
        return self._run_kubectl(args, kubeconfig=kubeconfig, input_data=input_data, insecure=True)

    def _get_guest_kubeconfig(self, cluster_id):
        """Generate a kubeconfig for accessing a guest cluster via Rancher proxy.

        Same approach as _get_rancher_kubeconfig but targets
        /k8s/clusters/{cluster_id} instead of /k8s/clusters/local.

        Args:
            cluster_id: Rancher management cluster ID (e.g. c-m-xxxxx)

        Returns:
            str: Path to temporary kubeconfig file
        """
        if cluster_id in self._guest_kubeconfig_paths:
            path = self._guest_kubeconfig_paths[cluster_id]
            if os.path.exists(path):
                return path

        token = self._authenticate_rancher()
        k8s_endpoint = (f"{self.rancher_endpoint.rstrip('/')}"
                        f"/k8s/clusters/{cluster_id}")

        kubeconfig = {
            "apiVersion": "v1",
            "kind": "Config",
            "clusters": [{
                "name": f"rancher-guest-{cluster_id}",
                "cluster": {
                    "server": k8s_endpoint,
                    "insecure-skip-tls-verify": True
                }
            }],
            "contexts": [{
                "name": f"rancher-guest-{cluster_id}",
                "context": {
                    "cluster": f"rancher-guest-{cluster_id}",
                    "user": "rancher-admin"
                }
            }],
            "current-context": f"rancher-guest-{cluster_id}",
            "users": [{
                "name": "rancher-admin",
                "user": {
                    "token": token
                }
            }]
        }

        fd, path = tempfile.mkstemp(
            suffix=".kubeconfig", prefix=f"guest-{cluster_id}-"
        )
        with os.fdopen(fd, 'w') as f:
            yaml.dump(kubeconfig, f)

        self._guest_kubeconfig_paths[cluster_id] = path
        logging(f"Created guest kubeconfig for {cluster_id}")
        return path

    def _run_kubectl_guest(self, cluster_id, args, input_data=None):
        """Run kubectl command against a guest cluster via Rancher proxy.

        Uses a dynamically generated kubeconfig that targets Rancher's
        K8s API at /k8s/clusters/{cluster_id}.

        NOTE: Do NOT use this for --raw requests. kubectl --raw strips the
        path component from the kubeconfig server URL, so requests go to
        the Rancher management cluster instead of the guest cluster.
        Use _run_kubectl_raw_guest for --raw API calls.

        Args:
            cluster_id: Rancher management cluster ID
            args: List of kubectl arguments
            input_data: Optional stdin data to pipe to kubectl

        Returns:
            tuple: (return_code, stdout, stderr)
        """
        kubeconfig = self._get_guest_kubeconfig(cluster_id)
        return self._run_kubectl(
            args, kubeconfig=kubeconfig, input_data=input_data, insecure=True
        )

    def _run_kubectl_raw_guest(self, cluster_id, raw_path, method="get",
                               input_data=None):
        """Run a kubectl --raw request against a guest cluster.

        kubectl --raw strips the path component from the kubeconfig server
        URL, so we use the Rancher management kubeconfig and prepend
        /k8s/clusters/{cluster_id} to the raw path ourselves.

        Args:
            cluster_id: Rancher management cluster ID
            raw_path: API path (e.g. /v1/catalog.cattle.io.clusterrepos/...)
            method: HTTP method — "get" or "create" (POST)
            input_data: Optional stdin data for POST requests

        Returns:
            tuple: (return_code, stdout, stderr)
        """
        full_path = f"/k8s/clusters/{cluster_id}{raw_path}"
        args = [method, "--raw", full_path]
        if method == "create" and input_data is not None:
            args.extend(["-f", "-"])
        return self._run_kubectl_rancher(args, input_data=input_data)

    def _run_kubectl(self, args, kubeconfig=None, context=None, input_data=None, insecure=False):
        """
        Run kubectl command and return output

        Args:
            args: List of kubectl arguments
            kubeconfig: Path to kubeconfig file (optional, uses default if not provided)
            context: Kubectl context to use (optional)
            input_data: Optional stdin data to pipe to kubectl
            insecure: Skip TLS certificate verification (for self-signed certs)

        Returns:
            tuple: (return_code, stdout, stderr)
        """
        cmd = ["kubectl"]
        if kubeconfig:
            cmd.extend(["--kubeconfig", kubeconfig])
        if context:
            cmd.extend(["--context", context])
        if insecure:
            cmd.append("--insecure-skip-tls-verify")
        cmd.extend(args)

        logging(f"Running kubectl command: {' '.join(cmd)}", level="DEBUG")
        result = subprocess.run(  # nosec B603
            cmd, capture_output=True, text=True, input=input_data
        )
        return result.returncode, result.stdout, result.stderr

    def create_harvester_mgmt_cluster(self, cluster_name):
        """
        Create Harvester management cluster entry in Rancher (Import Existing)

        Uses kubectl to create the provisioning.cattle.io/v1 Cluster CRD
        in Rancher's Kubernetes API.
        """
        logging(f"Creating Harvester management cluster entry: {cluster_name}")

        # Cluster CRD manifest
        cluster_manifest = {
            "apiVersion": "provisioning.cattle.io/v1",
            "kind": "Cluster",
            "metadata": {
                "name": cluster_name,
                "namespace": "fleet-default",
                "labels": {
                    "provider.cattle.io": "harvester"
                }
            },
            "spec": {}
        }

        # Write manifest to temp file
        fd, manifest_path = tempfile.mkstemp(suffix=".yaml", prefix="cluster-")
        with os.fdopen(fd, 'w') as f:
            yaml.dump(cluster_manifest, f)

        try:
            # Apply manifest using kubectl against Rancher
            rc, stdout, stderr = self._run_kubectl_rancher([
                "apply", "--validate=false", "-f", manifest_path
            ])

            if rc != 0:
                raise Exception(f"Failed to create Harvester mgmt cluster: {stderr}")

            logging(f"Created Harvester management cluster: {cluster_name}")

            # Return the created cluster
            return self.get_harvester_mgmt_cluster(cluster_name)
        finally:
            # Cleanup temp file
            if os.path.exists(manifest_path):
                os.remove(manifest_path)

    def get_harvester_mgmt_cluster(self, cluster_name):
        """
        Get Harvester management cluster details using kubectl.

        Uses kubectl to get the provisioning.cattle.io/v1 Cluster CRD
        from Rancher's Kubernetes API.
        """
        logging(f"Getting Harvester management cluster: {cluster_name}", level="DEBUG")

        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "clusters.provisioning.cattle.io",
            cluster_name, "-n", "fleet-default",
            "-o", "json"
        ])

        if rc != 0:
            if "NotFound" in stderr or "not found" in stderr.lower():
                logging(f"Harvester mgmt cluster {cluster_name} not found")
                return None
            raise Exception(f"Failed to get Harvester mgmt cluster: {stderr}")

        try:
            return json.loads(stdout)
        except json.JSONDecodeError as e:
            raise Exception(f"Failed to parse cluster response: {e}")

    def delete_harvester_mgmt_cluster(self, cluster_name):
        """
        Delete Harvester management cluster entry using kubectl.

        Uses kubectl to delete the provisioning.cattle.io/v1 Cluster CRD
        from Rancher's Kubernetes API.
        """
        logging(f"Deleting Harvester management cluster: {cluster_name}")

        rc, stdout, stderr = self._run_kubectl_rancher([
            "delete", "clusters.provisioning.cattle.io",
            cluster_name, "-n", "fleet-default",
            "--ignore-not-found"
        ])

        if rc != 0 and "NotFound" not in stderr and "not found" not in stderr.lower():
            raise Exception(f"Failed to delete Harvester mgmt cluster: {stderr}")

        logging(f"Deleted Harvester management cluster: {cluster_name}")

    def wait_for_harvester_ready(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """Wait for Harvester cluster to be ready in Rancher"""
        logging(f"Waiting for Harvester cluster {cluster_name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        iteration = 0
        while time.time() < end_time:
            try:
                cluster = self.get_harvester_mgmt_cluster(cluster_name)
                if cluster:
                    status = cluster.get("status", {})

                    # Check status.ready field (primary indicator)
                    if status.get("ready") is True:
                        logging(f"Harvester cluster {cluster_name} is ready (status.ready=true)")
                        return cluster

                    # Also check conditions for Ready type
                    conditions = status.get("conditions", [])
                    for condition in conditions:
                        if (condition.get("type") == "Ready" and
                                condition.get("status") == "True"):
                            logging(f"Harvester cluster {cluster_name} is ready (Ready condition)")
                            return cluster

                    # Log current state less frequently to avoid flooding
                    if iteration % 10 == 0:
                        ready_status = status.get("ready", "not set")
                        logging(
                            f"Cluster not ready yet. status.ready={ready_status}, "
                            f"conditions count={len(conditions)}"
                        )

            except Exception as e:
                if iteration % 10 == 0:
                    logging(f"Error checking cluster status: {e}", level="WARNING")

            iteration += 1
            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for Harvester cluster {cluster_name} to be ready")

    def wait_for_cluster_id(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """
        Wait for cluster to get its internal ID (status.clusterName).
        This appears shortly after cluster creation, before registration.
        """
        logging(f"Waiting for cluster {cluster_name} to get cluster ID")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            try:
                cluster = self.get_harvester_mgmt_cluster(cluster_name)
                if cluster:
                    cluster_id = cluster.get("status", {}).get("clusterName")
                    if cluster_id:
                        logging(f"Cluster {cluster_name} got ID: {cluster_id}")
                        return cluster
            except Exception as e:
                logging(f"Error checking cluster ID: {e}", level="WARNING")

            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for cluster {cluster_name} to get cluster ID")

    def _get_registration_token_secret_value(self, cluster_id, secret_name):
        """
        Read the registration token out of the Secret Rancher (>=2.15)
        stores it in.

        Args:
            cluster_id: Namespace the ClusterRegistrationToken/Secret live in
            secret_name: Name of the Secret (from status.tokenSecretName)

        Returns:
            str: The decoded token, or empty string if not yet available
        """
        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "secret", secret_name, "-n", cluster_id,
            "-o", "jsonpath={.data.token}"
        ])
        if rc != 0 or not stdout:
            return ""
        try:
            return base64.b64decode(stdout.strip()).decode("utf-8").strip()
        except (ValueError, UnicodeDecodeError) as e:
            logging(f"Failed to decode token secret {secret_name}: {e}", level="WARNING")
            return ""

    def get_cluster_registration_url(self, cluster_id, rancher_endpoint, timeout=300):
        """
        Get cluster registration URL for importing Harvester using kubectl.

        Polls for the ClusterRegistrationToken CRD (management.cattle.io/v3)
        which is automatically created by Rancher after cluster creation.

        The token lives in different places depending on the Rancher version:
        - Rancher <2.15: the plaintext token is in `.status.token` directly.
        - Rancher >=2.15: `.status.token` is left empty and the token is
          moved into a Secret named by `.status.tokenSecretName` (in the same
          namespace, base64-encoded under the `token` key) as a security
          hardening change.

        This method handles both cases and builds the manifest URL manually
        from the real token so it works regardless of Rancher version.

        Args:
            cluster_id: The internal cluster ID (e.g., c-m-xxxxx)
            rancher_endpoint: Rancher server endpoint used to build the URL
            timeout: Maximum time to wait for token

        Returns:
            The manifest URL for cluster registration
        """
        logging(f"Getting cluster registration URL for cluster: {cluster_id}")

        # ClusterRegistrationTokens are management.cattle.io/v3 resources
        # They're named as {cluster_id}:default-token in the cluster's namespace
        token_name = "default-token"

        retry_count, retry_interval = get_retry_count_and_interval()
        end_time = time.time() + int(timeout)
        attempt = 0

        while time.time() < end_time:
            attempt += 1
            try:
                # Get ClusterRegistrationToken status via kubectl
                rc, stdout, stderr = self._run_kubectl_rancher([
                    "get", "clusterregistrationtokens.management.cattle.io",
                    token_name, "-n", cluster_id,
                    "-o", "json"
                ])

                if rc == 0 and stdout:
                    status = json.loads(stdout).get("status", {})
                    token = (status.get("token") or "").strip()
                    secret_name = status.get("tokenSecretName")

                    if not token and secret_name:
                        # Rancher >=2.15: token lives in a Secret instead
                        token = self._get_registration_token_secret_value(cluster_id, secret_name)

                    # Log every 5 attempts to show progress
                    if attempt % 5 == 1:
                        logging(
                            f"Attempt {attempt}: "
                            f"token {'found' if token else 'not yet available'}"
                        )

                    if token and "{" not in token:
                        manifest_url = (
                            f"{rancher_endpoint.rstrip('/')}/v3/import/"
                            f"{token}_{cluster_id}.yaml"
                        )
                        logging(f"Got cluster registration URL: {manifest_url}")
                        return manifest_url
                elif "NotFound" in stderr or "not found" in stderr.lower():
                    if attempt % 5 == 1:
                        logging("Registration token not yet created, waiting...")
                else:
                    if attempt % 5 == 1:
                        logging(
                            f"Error getting registration token: {stderr}",
                            level="WARNING"
                        )
            except Exception as e:
                logging(
                    f"Error getting registration URL (attempt {attempt}): {e}",
                    level="WARNING"
                )

            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for cluster registration URL for {cluster_id} "
            f"after {attempt} attempts"
        )

    def set_cluster_registration_url(self, url):
        """Set cluster-registration-url setting in Harvester using kubectl.

        The value must be in JSON format matching Harvester structure:
        {'url': '...', 'insecureSkipTLSVerify': true/false}
        """
        logging(f"Setting cluster-registration-url to: {url}")

        # Format the value as JSON  object (matching Harvester's default structure)
        if url:
            value_obj = {"url": url, "insecureSkipTLSVerify": True}
            value = json.dumps(value_obj)
        else:
            value = '{"url":"","insecureSkipTLSVerify":false}'

        # Prepare patch data - the value itself is a JSON string
        patch_data = json.dumps({"value": value})

        # Patch the Harvester setting using the default (Harvester) kubeconfig
        rc, stdout, stderr = self._run_kubectl(
            ["patch", "settings.harvesterhci.io",
             "cluster-registration-url", "--type=merge", "-p", patch_data],
            insecure=True
        )

        if rc != 0:
            raise Exception(f"Failed to set cluster-registration-url: {stderr}")

        logging("Successfully set cluster-registration-url")

    def _get_rke2_versions_raw(self):
        """
        Fetch RKE2 release list via kubectl --raw against the Rancher local cluster.

        Rancher aggregates /v1-rke2-release/releases through its management
        cluster API, so it is accessible via the Rancher kubeconfig without
        any REST calls.

        Returns:
            list: Unsorted list of RKE2 version id strings
        """
        rc, stdout, stderr = self._run_kubectl_rancher(
            ["get", "--raw", "/v1-rke2-release/releases"]
        )
        if rc != 0:
            raise Exception(f"Failed to list RKE2 releases via kubectl: {stderr}")

        try:
            data = json.loads(stdout)
        except json.JSONDecodeError as e:
            raise Exception(f"Failed to parse RKE2 releases response: {e}")

        versions = [r["id"] for r in data.get("data", [])]
        if not versions:
            raise Exception("No RKE2 versions available from Rancher")
        return versions

    def get_all_rke2_versions(self, rancher_endpoint, max_versions=None):
        """
        Get all available RKE2 versions from Rancher.

        Args:
            rancher_endpoint: Rancher server endpoint
            max_versions: Maximum number of versions to return (None = all)

        Returns:
            List of version strings sorted by semantic version (newest first)
        """
        logging("Getting all RKE2 versions from Rancher")
        # Ensure we are authenticated so the Rancher kubeconfig is generated
        self._authenticate_rancher()

        versions = self._get_rke2_versions_raw()

        from pkg_resources import parse_version
        sorted_versions = sorted(
            versions,
            key=lambda v: parse_version(v.split("+")[0].split("-")[0]),
            reverse=True
        )

        if max_versions and max_versions > 0:
            sorted_versions = sorted_versions[:max_versions]

        logging(f"Found {len(sorted_versions)} RKE2 versions")
        return sorted_versions

    def get_rke2_version(self, target_version, rancher_endpoint):
        """
        Get RKE2 version from Rancher that matches target version.

        Args:
            target_version: Target version prefix (e.g. 'v1.28', 'v1.29')
            rancher_endpoint: Rancher server endpoint

        Returns:
            Full version string (e.g. 'v1.28.15+rke2r1')
        """
        logging(f"Getting RKE2 version for target: {target_version}")
        self._authenticate_rancher()

        versions = self._get_rke2_versions_raw()

        from pkg_resources import parse_version
        sorted_versions = sorted(
            versions,
            key=lambda v: parse_version(v.split("+")[0].split("-")[0]),
            reverse=True
        )

        for ver in sorted_versions:
            if ver.startswith(target_version):
                logging(f"Selected RKE2 version: {ver}")
                return ver

        raise Exception(
            f"No RKE2 version found matching '{target_version}'. "
            f"Available versions: {sorted_versions[:5]}"
        )

    def get_harvester_node_driver_version(self):
        """
        Get the Harvester node driver (docker-machine-driver-harvester) version.

        Parses the version out of the driver's release download URL
        (e.g. "...download/v1.0.6/docker-machine-driver-harvester-amd64.tar.gz").

        Returns:
            str: Version string (e.g. '1.0.6')
        """
        rc, stdout, stderr = self._run_kubectl_rancher(
            ["get", "--raw", "/v3/nodeDrivers/harvester"]
        )
        if rc != 0:
            raise Exception(f"Failed to get harvester node driver: {stderr}")

        url = json.loads(stdout).get("url", "")
        match = re.search(r"/v(\d+\.\d+\.\d+)/", url)
        if not match:
            raise Exception(f"Could not parse version from node driver url: {url}")
        return match.group(1)

    def configure_kdm_url(self, url):
        """
        Update the Rancher global setting rke-metadata-config to use a custom KDM URL.

        Reads the current setting value via kubectl, updates the 'url' field and
        sets 'refresh-interval-minutes' to '0', then patches it back so Rancher fetches
        the new data.json immediately.

        Args:
            url: URL of the custom KDM data.json file
        """
        logging(f"Configuring rke-metadata-config KDM URL: {url}")

        # Read current setting value
        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "settings.management.cattle.io", "rke-metadata-config",
            "-o", "json"
        ])
        if rc != 0:
            raise Exception(f"Failed to get rke-metadata-config: {stderr}")

        try:
            setting = json.loads(stdout)
        except json.JSONDecodeError as e:
            raise Exception(f"Failed to parse rke-metadata-config: {e}")

        current_value = setting.get("value") or setting.get("default", "{}")
        try:
            config = json.loads(current_value)
        except (json.JSONDecodeError, TypeError):
            config = {}

        config["url"] = url
        config["refresh-interval-minutes"] = "0"

        patch = json.dumps({"value": json.dumps(config)})
        rc, stdout, stderr = self._run_kubectl_rancher([
            "patch", "settings.management.cattle.io", "rke-metadata-config",
            "--type=merge", "-p", patch
        ])
        if rc != 0:
            raise Exception(f"Failed to update rke-metadata-config: {stderr}")

        logging(f"rke-metadata-config updated: url={url}, refresh-interval-minutes=0")

    def create_cloud_credential(self, name, kubeconfig, cluster_id):
        """Create cloud credential for Harvester"""
        logging(f"Creating cloud credential: {name}")
        # Cloud credentials are managed by Rancher, use Rancher kubeconfig
        secret_spec = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {
                "name": name,
                "namespace": "cattle-global-data",
                "annotations": {
                    "provisioning.cattle.io/driver": "harvester"
                }
            },
            "type": "Opaque",
            "stringData": {
                "harvestercredentialConfig-clusterId": cluster_id,
                "harvestercredentialConfig-clusterType": "imported",
                "harvestercredentialConfig-kubeconfigContent": kubeconfig
            }
        }

        returncode, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"],
            input_data=json.dumps(secret_spec)
        )
        if returncode != 0:
            raise Exception(f"Failed to create cloud credential: {stderr}")

        logging(f"Created cloud credential: {name}")
        return {"id": f"cattle-global-data:{name}", "metadata": {"name": name}}

    def get_cloud_credential(self, credential_id):
        """Get cloud credential details"""
        logging(f"Getting cloud credential: {credential_id}")
        parts = credential_id.split(":")
        namespace = parts[0] if len(parts) > 1 else "cattle-global-data"
        name = parts[-1]

        # Cloud credentials are in Rancher, use Rancher kubeconfig
        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "secret", name,
            "-n", namespace,
            "-o", "json"
        ])

        if rc == 0:
            return json.loads(stdout)
        elif "not found" in stderr.lower():
            return None
        else:
            raise Exception(f"Failed to get cloud credential: {stderr}")

    def delete_cloud_credential(self, credential_id):
        """Delete cloud credential"""
        logging(f"Deleting cloud credential: {credential_id}")
        parts = credential_id.split(":")
        namespace = parts[0] if len(parts) > 1 else "cattle-global-data"
        name = parts[-1]

        # Cloud credentials are in Rancher, use Rancher kubeconfig
        rc, stdout, stderr = self._run_kubectl_rancher([
            "delete", "secret", name,
            "-n", namespace,
            "--ignore-not-found=true"
        ])

        if rc == 0:
            logging(f"Deleted cloud credential: {credential_id}")
        else:
            raise Exception(f"Failed to delete cloud credential: {stderr}")

    def create_rke2_cluster(self, name, cloud_provider_config_id, hostname_prefix,
                            harvester_config_name, k8s_version, cloud_credential_id,
                            quantity, ingress="traefik"):
        """Create RKE2 cluster on Harvester"""
        logging(f"Creating RKE2 cluster: {name}")

        machine_global_config = {
            "cni": "calico",
            "disable-kube-proxy": False,
            "etcd-expose-metrics": False,
            "ingress-controller": ingress
        }

        drain_options = {
            "deleteEmptyDirData": True,
            "disableEviction": False,
            "enabled": False,
            "force": False,
            "gracePeriod": -1,
            "ignoreDaemonSets": True,
            "skipWaitForDeleteTimeoutSeconds": 0,
            "timeout": 120
        }

        cluster_spec = {
            "apiVersion": f"{self.rancher_provisioning_group}/{self.rancher_provisioning_version}",
            "kind": "Cluster",
            "metadata": {
                "name": name,
                "namespace": "fleet-default"
            },
            "spec": {
                "cloudCredentialSecretName": cloud_credential_id,
                "kubernetesVersion": k8s_version,
                "rkeConfig": {
                    "chartValues": {
                        "harvester-cloud-provider": {
                            "cloudConfigPath": (
                                "/var/lib/rancher/rke2/etc/config-files/"
                                "cloud-provider-config"
                            ),
                            "global": {
                                "cattle": {
                                    "clusterName": name
                                }
                            }
                        },
                        "rke2-calico": {},
                        "rke2-ingress-nginx": {},
                        "rke2-traefik": {}
                    },
                    "etcd": {
                        "snapshotRetention": 5,
                        "snapshotScheduleCron": "0 */5 * * *"
                    },
                    "machineGlobalConfig": machine_global_config,
                    "machinePools": [
                        {
                            "controlPlaneRole": True,
                            "etcdRole": True,
                            "workerRole": True,
                            "machineConfigRef": {
                                "kind": "HarvesterConfig",
                                "name": harvester_config_name
                            },
                            "name": "pool1",
                            "quantity": quantity,
                            "unhealthyNodeTimeout": "0s"
                        }
                    ],
                    "machineSelectorConfig": [
                        {
                            "config": {
                                "cloud-provider-config": f"secret://{cloud_provider_config_id}",
                                "cloud-provider-name": "harvester",
                                "protect-kernel-defaults": False
                            }
                        }
                    ],
                    "networking": {},
                    "registries": {},
                    "upgradeStrategy": {
                        "controlPlaneConcurrency": "1",
                        "controlPlaneDrainOptions": drain_options,
                        "workerConcurrency": "1",
                        "workerDrainOptions": drain_options
                    }
                }
            }
        }

        # RKE2 cluster is a Rancher resource, use Rancher kubeconfig
        returncode, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"],
            input_data=json.dumps(cluster_spec)
        )
        if returncode != 0:
            raise Exception(f"Failed to create RKE2 cluster: {stderr}")

        logging(f"Created RKE2 cluster: {name}")
        return cluster_spec

    def get_rke2_cluster(self, cluster_name):
        """Get RKE2 cluster details"""
        return self.get_harvester_mgmt_cluster(cluster_name)

    def delete_rke2_cluster(self, cluster_name):
        """Delete RKE2 cluster"""
        return self.delete_harvester_mgmt_cluster(cluster_name)

    def wait_for_rke2_cluster_ready(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """Wait for RKE2 cluster to be fully ready.

        Waits until:
        - The controller has processed the latest spec
          (status.observedGeneration >= metadata.generation)
        - status.ready is True
        - Updated and Provisioned conditions are True
        - All machines in cluster.x-k8s.io are in Running phase
        """
        logging(f"Waiting for RKE2 cluster {cluster_name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        iteration = 0
        while time.time() < end_time:
            try:
                cluster = self.get_rke2_cluster(cluster_name)
                if cluster:
                    metadata = cluster.get("metadata", {})
                    status = cluster.get("status", {})
                    spec = cluster.get("spec", {})

                    # Ensure controller has processed the latest spec
                    generation = metadata.get("generation", 0)
                    observed = status.get("observedGeneration", 0)
                    if observed < generation:
                        if iteration % 10 == 0:
                            logging(f"Controller hasn't processed spec yet: "
                                    f"observed={observed}, generation={generation}")
                        iteration += 1
                        time.sleep(retry_interval)
                        continue

                    ready = status.get("ready") is True

                    # Check conditions
                    conditions = {c.get("type"): c.get("status")
                                  for c in status.get("conditions", [])}
                    updated = conditions.get("Updated") == "True"
                    provisioned = conditions.get("Provisioned") == "True"

                    # Count desired machines from spec
                    desired_pools = spec.get("rkeConfig", {}).get("machinePools", [])
                    total_desired = sum(int(p.get("quantity", 0)) for p in desired_pools)

                    # Count actual running machines via cluster.x-k8s.io
                    total_running = self._count_running_machines(cluster_name)

                    # For custom clusters (no machinePools), require at least 1
                    effective_desired = total_desired if total_desired > 0 else 1

                    if (ready and updated and provisioned
                            and total_running >= effective_desired):
                        logging(f"RKE2 cluster {cluster_name} is fully ready "
                                f"(machines: {total_running}/{total_desired})")
                        return cluster

                    if iteration % 10 == 0:
                        logging(f"RKE2 cluster not fully ready yet. "
                                f"ready={ready}, updated={updated}, "
                                f"provisioned={provisioned}, "
                                f"machines={total_running}/{total_desired}")

            except Exception as e:
                if iteration % 10 == 0:
                    logging(f"Error checking RKE2 cluster status: {e}", level="WARNING")

            iteration += 1
            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for RKE2 cluster {cluster_name} to be ready")

    def _count_running_machines(self, cluster_name):
        """Count machines in Running phase for a given cluster."""
        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "machines.cluster.x-k8s.io",
            "-n", "fleet-default",
            "-l", f"cluster.x-k8s.io/cluster-name={cluster_name}",
            "-o", "jsonpath={.items[*].status.phase}"
        ])
        if rc != 0 or not stdout.strip():
            return 0
        phases = stdout.strip().split()
        return sum(1 for p in phases if p == "Running")

    def wait_for_rke2_cluster_deleted(self, cluster_name, timeout=DEFAULT_TIMEOUT_LONG):
        """Wait for RKE2 cluster to be deleted"""
        logging(f"Waiting for RKE2 cluster {cluster_name} to be deleted")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            cluster = self.get_rke2_cluster(cluster_name)
            if cluster is None:
                logging(f"RKE2 cluster {cluster_name} has been deleted")
                return True
            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for RKE2 cluster {cluster_name} to be deleted")

    def scale_rke2_cluster(self, cluster_name, worker_count, harvester_config_name):
        """Scale RKE2 cluster by adding/removing a worker-only machine pool.

        If worker_count > 0, a 'worker-pool' is added (or updated).
        If worker_count == 0, the 'worker-pool' is removed.
        """
        logging(f"Scaling RKE2 cluster {cluster_name}: worker_count={worker_count}")

        # Get current cluster spec
        cluster = self.get_rke2_cluster(cluster_name)
        if not cluster:
            raise Exception(f"RKE2 cluster {cluster_name} not found")

        machine_pools = cluster.get("spec", {}).get("rkeConfig", {}).get("machinePools", [])

        # Remove existing worker-pool if present
        machine_pools = [p for p in machine_pools if p.get("name") != "worker-pool"]

        if int(worker_count) > 0:
            # Add worker-only pool
            worker_pool = {
                "controlPlaneRole": False,
                "etcdRole": False,
                "workerRole": True,
                "machineConfigRef": {
                    "kind": "HarvesterConfig",
                    "name": harvester_config_name
                },
                "name": "worker-pool",
                "quantity": int(worker_count),
                "unhealthyNodeTimeout": "0s"
            }
            machine_pools.append(worker_pool)

        # Patch the cluster using strategic merge patch to avoid annotation overflow
        patch = {
            "spec": {
                "rkeConfig": {
                    "machinePools": machine_pools
                }
            }
        }

        rc, stdout, stderr = self._run_kubectl_rancher(
            ["patch", "clusters.provisioning.cattle.io", cluster_name,
             "-n", "fleet-default", "--type=merge",
             "-p", json.dumps(patch)]
        )
        if rc != 0:
            raise Exception(f"Failed to scale RKE2 cluster: {stderr}")

        logging(f"Scaled RKE2 cluster {cluster_name} worker pool to {worker_count}")

    def upgrade_rke2_cluster(self, cluster_name, new_k8s_version):
        """Upgrade RKE2 cluster to a new Kubernetes version."""
        logging(f"Upgrading RKE2 cluster {cluster_name} to {new_k8s_version}")

        cluster = self.get_rke2_cluster(cluster_name)
        if not cluster:
            raise Exception(f"RKE2 cluster {cluster_name} not found")

        patch = {
            "spec": {
                "kubernetesVersion": new_k8s_version
            }
        }

        rc, stdout, stderr = self._run_kubectl_rancher(
            ["patch", "clusters.provisioning.cattle.io", cluster_name,
             "-n", "fleet-default", "--type=merge",
             "-p", json.dumps(patch)]
        )
        if rc != 0:
            raise Exception(f"Failed to upgrade RKE2 cluster: {stderr}")

        logging(f"Triggered upgrade of RKE2 cluster {cluster_name} to {new_k8s_version}")

    def create_harvester_config(self, name, cpus, mems, disks, image_id,
                                network_id, ssh_user, user_data):
        """Create Harvester config for RKE2 node template"""
        logging(f"Creating Harvester config: {name}")

        import base64

        # Build diskInfo and networkInfo as JSON structures
        disk_info = json.dumps({
            "disks": [{
                "imageName": image_id,
                "bootOrder": 1,
                "size": int(disks)
            }]
        })

        network_info = json.dumps({
            "interfaces": [{
                "networkName": network_id,
                "macAddress": ""
            }]
        })

        # Base64-encode userData as expected by the HarvesterConfig API
        encoded_user_data = ""
        if user_data:
            encoded_user_data = base64.b64encode(
                user_data.encode("utf-8")
            ).decode("utf-8")

        config_spec = {
            "apiVersion": (
                f"{self.rancher_machine_config_group}/"
                f"{self.rancher_machine_config_version}"
            ),
            "kind": "HarvesterConfig",
            "metadata": {
                "name": name,
                "namespace": "fleet-default"
            },
            "cpuCount": str(cpus),
            "memorySize": str(mems),
            "diskInfo": disk_info,
            "diskSize": "0",
            "imageName": "",
            "networkInfo": network_info,
            "networkName": "",
            "reservedMemorySize": "-1",
            "sshUser": ssh_user,
            "userData": encoded_user_data,
            "vmNamespace": DEFAULT_NAMESPACE
        }

        # HarvesterConfig is a Rancher resource, use Rancher kubeconfig
        returncode, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"],
            input_data=json.dumps(config_spec)
        )
        if returncode != 0:
            raise Exception(f"Failed to create Harvester config: {stderr}")

        logging(f"Created Harvester config: {name}")
        return config_spec

    def generate_kubeconfig(self, cluster_id, cluster_name):
        """Generate full-access kubeconfig for Harvester cluster.

        Constructs a kubeconfig pointing to the Harvester cluster via Rancher's
        k8s proxy endpoint, using the existing admin bearer token. This is
        functionally equivalent to Rancher's generateKubeconfig action, which
        also returns a bearer-token kubeconfig for /k8s/clusters/{cluster_id}.
        """
        logging(f"Generating kubeconfig for cluster: {cluster_name}")

        token = self._authenticate_rancher()
        k8s_endpoint = (
            f"{self.rancher_endpoint.rstrip('/')}/k8s/clusters/{cluster_id}"
        )

        kubeconfig = {
            "apiVersion": "v1",
            "kind": "Config",
            "clusters": [{
                "name": cluster_name,
                "cluster": {
                    "server": k8s_endpoint,
                    "insecure-skip-tls-verify": True
                }
            }],
            "contexts": [{
                "name": cluster_name,
                "context": {
                    "cluster": cluster_name,
                    "user": cluster_name
                }
            }],
            "current-context": cluster_name,
            "users": [{
                "name": cluster_name,
                "user": {
                    "token": token
                }
            }]
        }

        kubeconfig_yaml = yaml.dump(kubeconfig)
        logging(f"Generated kubeconfig for cluster: {cluster_name}")
        return kubeconfig_yaml

    def generate_cloud_provider_kubeconfig(self, cluster_id, cluster_name):
        """Generate cloud provider kubeconfig via Harvester kubeconfig API.

        Uses kubectl create --raw to POST to the Harvester-specific kubeconfig
        endpoint through Rancher's k8s proxy, obtaining a kubeconfig with the
        limited cloudprovider role. This is used for the cloud provider secret
        inside the guest VM.
        """
        logging(f"Generating cloud provider kubeconfig for cluster: {cluster_name}")

        data = {
            "clusterRoleName": "harvesterhci.io:cloudprovider",
            "namespace": "default",
            "serviceAccountName": cluster_name
        }

        rc, stdout, stderr = self._run_kubectl_raw_guest(
            cluster_id,
            "/v1/harvester/kubeconfig",
            method="create",
            input_data=json.dumps(data)
        )

        if rc != 0:
            raise Exception(
                f"Failed to generate cloud provider kubeconfig: {stderr}"
            )

        kubeconfig = stdout.replace("\\n", "\n").strip('"')
        logging("Generated cloud provider kubeconfig for cluster")
        return kubeconfig

    def create_secret(self, name, data, annotations):
        """Create secret for cloud provider config"""
        logging(f"Creating secret: {name}")

        secret_spec = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {
                "name": name,
                "namespace": "fleet-default",
                "annotations": annotations
            },
            "type": "Opaque",
            "stringData": data
        }

        # Secret in fleet-default is a Rancher resource, use Rancher kubeconfig
        returncode, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"],
            input_data=json.dumps(secret_spec)
        )
        if returncode != 0:
            raise Exception(f"Failed to create secret: {stderr}")

        logging(f"Created secret: {name}")
        return {"metadata": {"namespace": "fleet-default", "name": name}}

    def create_deployment(self, cluster_id, namespace, name, image, pvc=None):
        """Create deployment in guest cluster"""
        logging(f"Creating deployment {name} in cluster {cluster_id}")

        container = {
            "name": name,
            "image": image,
            "ports": [{"containerPort": 80}]
        }

        deployment_spec = {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {
                "name": name,
                "namespace": namespace
            },
            "spec": {
                "replicas": 1,
                "selector": {
                    "matchLabels": {
                        "name": name
                    }
                },
                "template": {
                    "metadata": {
                        "labels": {
                            "name": name
                        }
                    },
                    "spec": {
                        "containers": [container]
                    }
                }
            }
        }

        if pvc:
            deployment_spec["spec"]["template"]["spec"]["volumes"] = [
                {"name": "data", "persistentVolumeClaim": {"claimName": pvc}}
            ]
            deployment_spec["spec"]["template"]["spec"]["containers"][0]["volumeMounts"] = [
                {"name": "data", "mountPath": "/data"}
            ]

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(deployment_spec)
        )
        if rc != 0:
            raise Exception(f"Failed to create deployment: {stderr}")

        logging(f"Created deployment {name}")
        return deployment_spec

    def get_deployment(self, cluster_id, namespace, name):
        """Get deployment details from guest cluster via kubectl"""

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["get", "deployment", name, "-n", namespace, "-o", "json"]
        )

        if rc != 0:
            if "NotFound" in stderr or "not found" in stderr.lower():
                return None
            raise Exception(f"Failed to get deployment: {stderr}")

        return json.loads(stdout)

    def delete_deployment(self, cluster_id, namespace, name):
        """Delete deployment from guest cluster via kubectl"""
        logging(f"Deleting deployment {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "deployment", name, "-n", namespace,
             "--ignore-not-found"]
        )

        if rc != 0:
            raise Exception(f"Failed to delete deployment: {stderr}")

        logging(f"Deleted deployment {name}")

    def wait_for_deployment_ready(self, cluster_id, namespace, name, timeout=DEFAULT_TIMEOUT):
        """Wait for deployment to be ready"""
        logging(f"Waiting for deployment {name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        last_log_time = 0
        while time.time() < end_time:
            try:
                deployment = self.get_deployment(cluster_id, namespace, name)
                if deployment:
                    status = deployment.get("status", {})
                    replicas = status.get("replicas", 0)
                    available = status.get("availableReplicas", 0)
                    if replicas > 0 and available >= replicas:
                        logging(f"Deployment {name} is ready")
                        return deployment
                    now = time.time()
                    if now - last_log_time >= 30:
                        logging(f"Deployment {name}: {available}/{replicas} available")
                        last_log_time = now
            except Exception as e:
                logging(f"Error checking deployment status: {e}", level="WARNING")

            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for deployment {name} to be ready")

    def wait_for_deployment_deleted(self, cluster_id, namespace, name, timeout=DEFAULT_TIMEOUT):
        """Wait for deployment to be deleted"""
        logging(f"Waiting for deployment {name} to be deleted")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            deployment = self.get_deployment(cluster_id, namespace, name)
            if deployment is None:
                logging(f"Deployment {name} has been deleted")
                return True
            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for deployment {name} to be deleted")

    def scale_deployment(self, cluster_id, namespace, name, replicas):
        """Scale a deployment in a guest cluster to the given replica count"""
        logging(f"Scaling deployment {name} to {replicas} replicas "
                f"in cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["scale", "deployment", name, "-n", namespace,
             f"--replicas={replicas}"]
        )

        if rc != 0:
            raise Exception(f"Failed to scale deployment {name}: {stderr}")

        logging(f"Scaled deployment {name} to {replicas} replicas")

    def wait_for_deployment_scaled(self, cluster_id, namespace, name,
                                   replicas, timeout=DEFAULT_TIMEOUT):
        """Wait for a deployment to reach the given ready replica count"""
        logging(f"Waiting for deployment {name} to scale to {replicas}")
        retry_count, retry_interval = get_retry_count_and_interval()
        replicas = int(replicas)

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            try:
                deployment = self.get_deployment(cluster_id, namespace, name)
                if deployment:
                    status = deployment.get("status", {})
                    ready = status.get("readyReplicas", 0)
                    if ready == replicas:
                        logging(f"Deployment {name} scaled to {replicas}")
                        return deployment
            except Exception as e:
                logging(f"Error checking deployment scale: {e}",
                        level="WARNING")

            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for deployment {name} to scale to {replicas}"
        )

    def create_pvc(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create PVC in guest cluster via kubectl"""
        logging(f"Creating PVC {name} in cluster {cluster_id}")

        pvc_spec = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": {
                "name": name,
                "namespace": DEFAULT_NAMESPACE
            },
            "spec": {
                "accessModes": ["ReadWriteOnce"],
                "resources": {
                    "requests": {
                        "storage": size
                    }
                }
            }
        }
        if storage_class:
            pvc_spec["spec"]["storageClassName"] = storage_class

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(pvc_spec)
        )
        if rc != 0:
            raise Exception(f"Failed to create PVC: {stderr}")

        logging(f"Created PVC {name}")
        return pvc_spec

    def get_pvc(self, cluster_id, name):
        """Get PVC details from guest cluster via kubectl"""
        logging(f"Getting PVC {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["get", "pvc", name, "-n", DEFAULT_NAMESPACE, "-o", "json"]
        )

        if rc != 0:
            if "NotFound" in stderr or "not found" in stderr.lower():
                return None
            raise Exception(f"Failed to get PVC: {stderr}")

        return json.loads(stdout)

    def delete_pvc(self, cluster_id, name):
        """Delete PVC from guest cluster via kubectl"""
        logging(f"Deleting PVC {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "pvc", name, "-n", DEFAULT_NAMESPACE,
             "--ignore-not-found"]
        )
        if rc != 0:
            raise Exception(f"Failed to delete PVC: {stderr}")

        logging(f"Deleted PVC {name}")

    def wait_for_pvc_bound(self, cluster_id, name, timeout=DEFAULT_TIMEOUT):
        """Wait for PVC to be bound"""
        logging(f"Waiting for PVC {name} to be bound")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            try:
                pvc = self.get_pvc(cluster_id, name)
                if pvc:
                    phase = pvc.get("status", {}).get("phase", "")
                    if phase == "Bound":
                        logging(f"PVC {name} is bound")
                        return pvc
            except Exception as e:
                logging(f"Error checking PVC status: {e}", level="WARNING")

            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for PVC {name} to be bound")

    def create_lb_service(self, cluster_id, service_data):
        """Create LoadBalancer service in guest cluster via kubectl"""
        name = service_data["metadata"]["name"]
        logging(f"Creating LoadBalancer service {name} in cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(service_data)
        )
        if rc != 0:
            raise Exception(f"Failed to create LoadBalancer service: {stderr}")

        logging(f"Created LoadBalancer service {name}")
        return service_data

    def get_lb_service(self, cluster_id, name):
        """Get LoadBalancer service details from guest cluster via kubectl"""
        logging(f"Getting LoadBalancer service {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["get", "service", name, "-n", DEFAULT_NAMESPACE, "-o", "json"]
        )

        if rc != 0:
            if "NotFound" in stderr or "not found" in stderr.lower():
                return None
            raise Exception(f"Failed to get LoadBalancer service: {stderr}")

        return json.loads(stdout)

    def delete_lb_service(self, cluster_id, name):
        """Delete LoadBalancer service from guest cluster via kubectl"""
        logging(f"Deleting LoadBalancer service {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "service", name, "-n", DEFAULT_NAMESPACE,
             "--ignore-not-found"]
        )
        if rc != 0:
            raise Exception(f"Failed to delete LoadBalancer service: {stderr}")

        logging(f"Deleted LoadBalancer service {name}")

    def wait_for_lb_service_ready(self, cluster_id, name, timeout=DEFAULT_TIMEOUT):
        """Wait for LoadBalancer service to be ready (IP assigned)"""
        logging(f"Waiting for LoadBalancer service {name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            try:
                service = self.get_lb_service(cluster_id, name)
                if service:
                    ingress = (service.get("status", {})
                               .get("loadBalancer", {})
                               .get("ingress", []))
                    if ingress and ingress[0].get("ip"):
                        ip = ingress[0]["ip"]
                        logging(f"LoadBalancer service {name} is ready with IP {ip}")
                        return service
            except Exception as e:
                logging(f"Error checking LoadBalancer status: {e}", level="WARNING")

            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for LoadBalancer service {name} to be ready")

    def query_lb_service(self, url, retries=10, interval=5):
        """Query LoadBalancer service endpoint with retries"""
        logging(f"Querying LoadBalancer service at: {url}")
        last_err = None
        for attempt in range(retries):
            try:
                response = requests.get(url, timeout=30, verify=False)  # nosec B501
                return response.status_code, response.text
            except Exception as e:
                last_err = e
                if attempt < retries - 1:
                    logging(f"LB query attempt {attempt + 1}/{retries} failed: {e}, retrying...")
                    time.sleep(interval)
        raise Exception(
            f"Failed to query LoadBalancer service after {retries} attempts: {last_err}"
        )

    def query_lb_via_proxy(self, cluster_id, service_name, port=8080,
                           namespace="default", retries=10, interval=5):
        """Query LoadBalancer service via Rancher's k8s service proxy.

        Uses _run_kubectl_raw_guest to access the service proxy endpoint.
        """
        proxy_path = (f"/api/v1/namespaces/{namespace}/services/"
                      f"{service_name}:{port}/proxy/")
        logging(f"Querying LB via kubectl raw: {proxy_path}")
        last_err = None
        for attempt in range(retries):
            try:
                rc, stdout, stderr = self._run_kubectl_raw_guest(
                    cluster_id, proxy_path
                )
                if rc == 0:
                    return 200, stdout
                last_err = stderr
            except Exception as e:
                last_err = e
            if attempt < retries - 1:
                logging(f"LB proxy query attempt {attempt + 1}/{retries} "
                        f"failed: {last_err}, retrying...")
                time.sleep(interval)
        raise Exception(
            f"Failed to query LB via proxy after {retries} attempts: {last_err}"
        )

    def wait_for_harvester_deployments_ready(self, cluster_id, timeout=DEFAULT_TIMEOUT):
        """Wait for harvester-cloud-provider and harvester-csi-driver to be ready"""
        logging(f"Waiting for Harvester deployments in cluster {cluster_id}")
        deployments = ["harvester-cloud-provider", "harvester-csi-driver-controllers"]

        for deployment_name in deployments:
            self.wait_for_deployment_ready(cluster_id, "kube-system", deployment_name, timeout)

        logging("All Harvester deployments are ready")

    # Import Existing Cluster Operations
    def create_import_cluster(self, name):
        """Create a minimal provisioning cluster for import."""
        logging(f"Creating import cluster: {name}")

        cluster_spec = {
            "apiVersion": (
                f"{self.rancher_provisioning_group}"
                f"/{self.rancher_provisioning_version}"
            ),
            "kind": "Cluster",
            "metadata": {
                "name": name,
                "namespace": "fleet-default"
            },
            "spec": {}
        }

        returncode, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"],
            input_data=json.dumps(cluster_spec)
        )
        if returncode != 0:
            raise Exception(
                f"Failed to create import cluster: {stderr}"
            )

        logging(f"Created import cluster: {name}")
        return cluster_spec

    def wait_for_import_cluster_ready(self, cluster_name,
                                      timeout=DEFAULT_TIMEOUT_LONG):
        """Wait for an imported cluster to become active in Rancher.

        Does NOT check machinePools or cluster.x-k8s.io machines.
        """
        logging(f"Waiting for import cluster {cluster_name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        iteration = 0
        while time.time() < end_time:
            try:
                cluster = self.get_rke2_cluster(cluster_name)
                if cluster:
                    metadata = cluster.get("metadata", {})
                    status = cluster.get("status", {})

                    generation = metadata.get("generation", 0)
                    observed = status.get("observedGeneration", 0)
                    if observed < generation:
                        if iteration % 10 == 0:
                            logging(
                                f"Controller hasn't processed spec yet: "
                                f"observed={observed}, "
                                f"generation={generation}"
                            )
                        iteration += 1
                        time.sleep(retry_interval)
                        continue

                    ready = status.get("ready") is True
                    cluster_id = status.get("clusterName")

                    connected = False
                    ready_cond = False
                    if cluster_id:
                        rc, mgmt_stdout, mgmt_stderr = self._run_kubectl_rancher([
                            "get", "clusters.management.cattle.io",
                            cluster_id, "-o", "json"
                        ])
                        if rc == 0 and mgmt_stdout:
                            try:
                                mgmt_status = json.loads(mgmt_stdout).get("status", {})
                                mgmt_conditions = {
                                    c.get("type"): c.get("status")
                                    for c in mgmt_status.get("conditions", [])
                                }
                                connected = mgmt_conditions.get("Connected") == "True"
                                ready_cond = mgmt_conditions.get("Ready") == "True"
                            except json.JSONDecodeError:
                                pass

                    if ready and (connected or ready_cond):
                        logging(
                            f"Import cluster {cluster_name} is ready"
                        )
                        return cluster

                    if iteration % 10 == 0:
                        logging(
                            f"Import cluster not ready yet. "
                            f"ready={ready}, cluster_id={cluster_id}, "
                            f"connected={connected}, ready_cond={ready_cond}"
                        )

            except Exception as e:
                if iteration % 10 == 0:
                    logging(
                        f"Error checking import cluster status: {e}",
                        level="WARNING"
                    )

            iteration += 1
            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for import cluster {cluster_name} to be ready"
        )

    # Custom RKE2 Cluster Operations
    def create_custom_rke2_cluster(self, name, cloud_provider_config_id,
                                   k8s_version, cloud_credential_id,
                                   ingress="traefik"):
        """Create a custom RKE2 cluster without machinePools."""
        logging(f"Creating custom RKE2 cluster: {name}")

        machine_global_config = {
            "cni": "calico",
            "disable-kube-proxy": False,
            "etcd-expose-metrics": False,
            "ingress-controller": ingress
        }

        drain_options = {
            "deleteEmptyDirData": True,
            "disableEviction": False,
            "enabled": False,
            "force": False,
            "gracePeriod": -1,
            "ignoreDaemonSets": True,
            "skipWaitForDeleteTimeoutSeconds": 0,
            "timeout": 120
        }

        cluster_spec = {
            "apiVersion": (
                f"{self.rancher_provisioning_group}"
                f"/{self.rancher_provisioning_version}"
            ),
            "kind": "Cluster",
            "metadata": {
                "name": name,
                "namespace": "fleet-default"
            },
            "spec": {
                "cloudCredentialSecretName": cloud_credential_id,
                "kubernetesVersion": k8s_version,
                "rkeConfig": {
                    "chartValues": {
                        "harvester-cloud-provider": {
                            "cloudConfigPath": (
                                "/var/lib/rancher/rke2/etc/config-files/"
                                "cloud-provider-config"
                            ),
                            "global": {
                                "cattle": {
                                    "clusterName": name
                                }
                            }
                        },
                        "rke2-calico": {},
                        "rke2-ingress-nginx": {},
                        "rke2-traefik": {}
                    },
                    "etcd": {
                        "snapshotRetention": 5,
                        "snapshotScheduleCron": "0 */5 * * *"
                    },
                    "machineGlobalConfig": machine_global_config,
                    "machineSelectorConfig": [
                        {
                            "config": {
                                "cloud-provider-config": (
                                    f"secret://{cloud_provider_config_id}"
                                ),
                                "cloud-provider-name": "harvester",
                                "protect-kernel-defaults": False
                            }
                        }
                    ],
                    "networking": {},
                    "registries": {},
                    "upgradeStrategy": {
                        "controlPlaneConcurrency": "1",
                        "controlPlaneDrainOptions": drain_options,
                        "workerConcurrency": "1",
                        "workerDrainOptions": drain_options
                    }
                }
            }
        }

        returncode, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"],
            input_data=json.dumps(cluster_spec)
        )
        if returncode != 0:
            raise Exception(
                f"Failed to create custom RKE2 cluster: {stderr}"
            )

        logging(f"Created custom RKE2 cluster: {name}")
        return cluster_spec

    def update_cluster_chart_name(self, cluster_name, mgmt_cluster_id):
        """Patch the custom cluster's chartValues with the real management ID."""
        logging(f"Updating clusterName to {mgmt_cluster_id} for {cluster_name}")

        patch = {
            "spec": {
                "rkeConfig": {
                    "chartValues": {
                        "harvester-cloud-provider": {
                            "global": {
                                "cattle": {
                                    "clusterName": mgmt_cluster_id
                                }
                            }
                        }
                    }
                }
            }
        }

        rc, stdout, stderr = self._run_kubectl_rancher(
            ["patch", "clusters.provisioning.cattle.io", cluster_name,
             "-n", "fleet-default", "--type=merge",
             "-p", json.dumps(patch)]
        )

        if rc != 0:
            raise Exception(
                f"Failed to update clusterName for {cluster_name}: "
                f"{stderr[:500]}"
            )

        logging(f"Updated clusterName to {mgmt_cluster_id}")

    def fix_cloud_provider_cluster_name(self, cluster_id):
        """Fix the cloud-provider --cluster-name arg on the guest cluster.

        Rancher's provisioning controller deploys the cloud-provider chart
        with the provisioning name instead of the management cluster ID.
        The cloud provider uses --cluster-name to match VMs via the
        harvesterhci.io/owned-by label, which Rancher sets to the mgmt ID.

        This waits for the deployment to exist, patches the arg, then waits
        for the rolling update to complete.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(f"Fixing cloud provider clusterName to {cluster_id}")

        # Wait for the cloud-provider deployment to exist
        deploy = self.wait_for_deployment_ready(
            cluster_id, "kube-system", "harvester-cloud-provider"
        )

        containers = deploy["spec"]["template"]["spec"]["containers"]

        for idx, container in enumerate(containers):
            if "cloud-provider" not in container.get("name", ""):
                continue

            args = container.get("args", [])
            json_patch = None

            for arg_idx, arg in enumerate(args):
                if arg.startswith("--cluster-name"):
                    current = arg.split("=", 1)[1] if "=" in arg else ""
                    if current == cluster_id:
                        logging(f"clusterName already correct: {cluster_id}")
                        return
                    json_patch = [{
                        "op": "replace",
                        "path": (
                            f"/spec/template/spec/containers/{idx}"
                            f"/args/{arg_idx}"
                        ),
                        "value": f"--cluster-name={cluster_id}"
                    }]
                    break

            if json_patch is None:
                # --cluster-name not present, add it
                json_patch = [{
                    "op": "add",
                    "path": (
                        f"/spec/template/spec/containers/{idx}/args/-"
                    ),
                    "value": f"--cluster-name={cluster_id}"
                }]

            rc, stdout, stderr = self._run_kubectl_guest(
                cluster_id,
                ["patch", "deployment", "harvester-cloud-provider",
                 "-n", "kube-system", "--type=json",
                 "-p", json.dumps(json_patch)]
            )

            if rc != 0:
                raise Exception(
                    f"Failed to patch cloud-provider: {stderr[:300]}"
                )

            logging(f"Patched --cluster-name to {cluster_id}, "
                    f"waiting for rollout")

            # Wait for the rolling update to complete
            rc, stdout, stderr = self._run_kubectl_guest(
                cluster_id,
                ["rollout", "status",
                 "deployment/harvester-cloud-provider",
                 "-n", "kube-system", "--timeout=300s"]
            )
            if rc != 0:
                logging(f"Rollout warning: {stderr[:200]}",
                        level="WARNING")

            logging("Cloud provider deployment rolled out with "
                    "correct clusterName")
            return

        raise Exception(
            "Could not find cloud-provider container in deployment"
        )

    def get_cluster_registration_command(self, cluster_name,
                                         timeout=DEFAULT_TIMEOUT_LONG):
        """Get the insecure node registration command for a custom cluster.
        """
        logging(f"Getting registration command for cluster: {cluster_name}")

        token = self._authenticate_rancher()

        # Get the management cluster ID
        cluster = self.get_rke2_cluster(cluster_name)
        if not cluster:
            raise Exception(f"Cluster {cluster_name} not found")

        cluster_id = cluster.get("status", {}).get("clusterName", "")
        if not cluster_id:
            raise Exception(
                f"Cluster {cluster_name} has no management cluster ID yet"
            )

        logging(f"Management cluster ID: {cluster_id}")

        url = (f"{self.rancher_endpoint.rstrip('/')}/v3/clusterRegistrationTokens/"
               f"{cluster_id}:default-token")
        headers = {"Authorization": f"Bearer {token}"}

        retry_count, retry_interval = get_retry_count_and_interval()
        end_time = time.time() + int(timeout)
        attempt = 0

        while time.time() < end_time:
            attempt += 1
            try:
                response = requests.get(url, headers=headers, verify=False)  # nosec B501
            except Exception as e:
                logging(
                    f"Error getting registration command "
                    f"(attempt {attempt}): {e}",
                    level="WARNING"
                )
                time.sleep(retry_interval)
                continue

            if attempt % 5 == 1 or response.status_code not in [200, 404]:
                logging(f"Attempt {attempt}: Response status {response.status_code}")

            if response.status_code == 200:
                node_cmd = response.json().get("insecureNodeCommand", "")
                if node_cmd:
                    logging("Got cluster registration command")
                    return node_cmd
                elif attempt % 5 == 1:
                    logging("insecureNodeCommand not yet available, waiting...")
            elif response.status_code == 404:
                if attempt % 5 == 1:
                    logging("Registration token not yet created, waiting...")
            else:
                logging(
                    f"Unexpected response {response.status_code}: {response.text}",
                    level="WARNING"
                )

            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for registration command for {cluster_name} "
            f"after {attempt} attempts"
        )

    # Harvester VM Operations (for custom cluster nodes)
    def create_harvester_vm(self, name, image_id, network_id, cpus, memory,
                            disk_size, ssh_user, user_data, network_data="",
                            guest_cluster_id=""):
        """Create a VM on Harvester using the Kubernetes custom API.

        Builds the VirtualMachine manifest directly (same pattern as vm/crd.py)
        to avoid any dependency on the harvester_api package, which is shadowed
        by libs/harvester_api.py when Robot Framework prepends libs/ to sys.path.
        """
        logging(f"Creating Harvester VM: {name}")

        disk_name = f"{name}-disk-0"
        disk_size_gi = f"{disk_size}Gi"

        # Look up the image's actual storage class from Harvester.
        # Harvester dynamically creates a storage class (lh-<uuid>) per image;
        # the name cannot be guessed — it must be read from the image status.
        image_ns, image_name = (
            image_id.split("/", 1) if "/" in image_id
            else (DEFAULT_NAMESPACE, image_id)
        )
        try:
            img_obj = self.custom_api.get_namespaced_custom_object(
                group="harvesterhci.io",
                version="v1beta1",
                namespace=image_ns,
                plural="virtualmachineimages",
                name=image_name
            )
            storage_class = img_obj.get("status", {}).get("storageClassName", "")
            if not storage_class:
                raise Exception(
                    f"Image {image_id} has no storageClassName in status"
                )
            logging(f"Resolved storage class for image {image_id}: {storage_class}")
        except ApiException as e:
            raise Exception(
                f"Failed to look up image {image_id} for storage class: {e}"
            )

        volume_claim_templates = [
            {
                "metadata": {
                    "name": disk_name,
                    "annotations": {
                        "harvesterhci.io/imageId": image_id
                    }
                },
                "spec": {
                    "accessModes": ["ReadWriteMany"],
                    "resources": {"requests": {"storage": disk_size_gi}},
                    "volumeMode": "Block",
                    "storageClassName": storage_class
                }
            }
        ]

        # Build network interfaces: when a VLAN network is provided, use only
        # the bridge interface on the VLAN (matching Harvester UI behaviour).
        # This gives the VM a routable VLAN IP as its primary address.
        # Fall back to masquerade/pod network when no VLAN is specified.
        if network_id:
            interfaces = [
                {"bridge": {}, "model": "virtio", "name": "default"}
            ]
            networks = [
                {"name": "default", "multus": {"networkName": network_id}}
            ]
        else:
            interfaces = [
                {"masquerade": {}, "model": "virtio", "name": "default"}
            ]
            networks = [{"name": "default", "pod": {}}]

        # Volumes: root disk + cloud-init
        volumes = [
            {"name": "rootdisk", "persistentVolumeClaim": {"claimName": disk_name}},
            {
                "name": "cloudinitdisk",
                "cloudInitNoCloud": {
                    "userData": user_data or "#cloud-config\n",
                    **({
                        "networkData": network_data
                    } if network_data else {})
                }
            }
        ]

        disks = [
            {"name": "rootdisk", "disk": {"bus": "virtio"}, "bootOrder": 1},
            {"name": "cloudinitdisk", "disk": {"bus": "virtio"}}
        ]

        memory_gi = f"{memory}Gi" if str(memory).isdigit() else str(memory)

        # VM-level labels
        vm_labels = {
            "harvesterhci.io/creator": "robot-framework",
            "harvesterhci.io/os": "linux"
        }
        # Pod template labels
        pod_labels = {"harvesterhci.io/vmName": name}

        # When a guest cluster ID is provided, add the labels the
        # Harvester LB webhook requires to discover guest-cluster VMs.
        if guest_cluster_id:
            vm_labels["harvesterhci.io/creator"] = (
                "docker-machine-driver-harvester"
            )
            vm_labels["guestcluster.harvesterhci.io/name"] = (
                guest_cluster_id
            )
            pod_labels["harvesterhci.io/creator"] = (
                "docker-machine-driver-harvester"
            )
            pod_labels["guestcluster.harvesterhci.io/name"] = (
                guest_cluster_id
            )

        vm_dict = {
            "apiVersion": "kubevirt.io/v1",
            "kind": "VirtualMachine",
            "metadata": {
                "name": name,
                "namespace": DEFAULT_NAMESPACE,
                "annotations": {
                    "harvesterhci.io/vmRunStrategy": "RerunOnFailure",
                    "harvesterhci.io/volumeClaimTemplates": (
                        json.dumps(volume_claim_templates)
                    ),
                    "harvesterhci.io/sshNames": "[]"
                },
                "labels": vm_labels
            },
            "spec": {
                "runStrategy": "RerunOnFailure",
                "template": {
                    "metadata": {
                        "annotations": {"harvesterhci.io/sshNames": "[]"},
                        "labels": pod_labels
                    },
                    "spec": {
                        "affinity": {},
                        "architecture": "amd64",
                        "domain": {
                            "cpu": {
                                "cores": int(cpus),
                                "sockets": 1,
                                "threads": 1
                            },
                            "devices": {
                                "inputs": [
                                    {"bus": "usb", "name": "tablet",
                                     "type": "tablet"}
                                ],
                                "interfaces": interfaces,
                                "disks": disks
                            },
                            "features": {"acpi": {"enabled": True}},
                            "machine": {"type": "q35"},
                            "memory": {"guest": memory_gi},
                            "resources": {
                                "limits": {
                                    "cpu": str(cpus),
                                    "memory": memory_gi
                                },
                                "requests": {
                                    "cpu": "125m",
                                    "memory": "2730Mi"
                                }
                            }
                        },
                        "evictionStrategy": "LiveMigrateIfPossible",
                        "hostname": name,
                        "networks": networks,
                        "terminationGracePeriodSeconds": 120,
                        "volumes": volumes
                    }
                }
            }
        }

        try:
            self.custom_api.create_namespaced_custom_object(
                group="kubevirt.io",
                version="v1",
                namespace=DEFAULT_NAMESPACE,
                plural="virtualmachines",
                body=vm_dict
            )
            logging(f"Created Harvester VM: {name}")
            return vm_dict
        except ApiException as e:
            raise Exception(f"Failed to create VM {name}: {e}")

    # Chart Install Operations (for import clusters)
    def install_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Install a Helm chart on a guest cluster via Rancher catalog API.

        Uses _run_kubectl_raw_guest to POST to the Steve catalog install
        action on the correct guest cluster.

        Args:
            cluster_id: Rancher management cluster ID (e.g. c-m-xxxxx)
            repo_name: Chart repository name (e.g. rancher-charts)
            chart_name: Chart name (e.g. harvester-cloud-provider)
            version: Chart version
            release_name: Helm release name
            namespace: Target namespace for the chart
            values: Optional dict of chart values overrides
        """
        logging(f"Installing chart {chart_name} {version} as {release_name} "
                f"in {namespace} on cluster {cluster_id}")

        payload = {
            "charts": [
                {
                    "chartName": chart_name,
                    "version": version,
                    "releaseName": release_name,
                    "annotations": {
                        "catalog.cattle.io/ui-source-repo": repo_name,
                        "catalog.cattle.io/ui-source-repo-type": "cluster"
                    },
                    "values": values or {}
                }
            ],
            "namespace": namespace
        }

        rc, stdout, stderr = self._run_kubectl_raw_guest(
            cluster_id,
            f"/v1/catalog.cattle.io.clusterrepos/{repo_name}"
            f"?action=install",
            method="create",
            input_data=json.dumps(payload)
        )

        if rc != 0:
            raise Exception(
                f"Failed to install chart {chart_name}: {stderr[:500]}"
            )

        logging(f"Chart install initiated: {chart_name} {version}")
        return json.loads(stdout) if stdout.strip() else {}

    def upgrade_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Upgrade an installed Helm chart on a guest cluster via Rancher
        catalog API.

        Args:
            cluster_id: Rancher management cluster ID (e.g. c-m-xxxxx)
            repo_name: Chart repository name (e.g. rancher-charts)
            chart_name: Chart name (e.g. harvester-csi-driver)
            version: Target chart version
            release_name: Helm release name
            namespace: Namespace where the chart is installed
            values: Optional dict of chart values overrides
        """
        logging(f"Upgrading chart {chart_name} to {version} as "
                f"{release_name} in {namespace} on cluster {cluster_id}")

        payload = {
            "charts": [
                {
                    "chartName": chart_name,
                    "version": version,
                    "releaseName": release_name,
                    "annotations": {
                        "catalog.cattle.io/ui-source-repo": repo_name,
                        "catalog.cattle.io/ui-source-repo-type": "cluster"
                    },
                    "values": values or {}
                }
            ],
            "namespace": namespace
        }

        rc, stdout, stderr = self._run_kubectl_raw_guest(
            cluster_id,
            f"/v1/catalog.cattle.io.clusterrepos/{repo_name}"
            f"?action=upgrade",
            method="create",
            input_data=json.dumps(payload)
        )

        if rc != 0:
            raise Exception(
                f"Failed to upgrade chart {chart_name}: {stderr[:500]}"
            )

        logging(f"Chart upgrade initiated: {chart_name} v{version}")
        return json.loads(stdout) if stdout.strip() else {}

    def uninstall_chart(self, cluster_id, release_name, namespace):
        """Uninstall a Helm chart (app) from a guest cluster via Rancher
        catalog API.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name
            namespace: Namespace where the chart is installed
        """
        logging(f"Uninstalling chart {release_name} from {namespace} "
                f"on cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_raw_guest(
            cluster_id,
            f"/v1/catalog.cattle.io.apps/{namespace}/{release_name}"
            f"?action=uninstall",
            method="create",
            input_data=json.dumps({})
        )

        if rc != 0:
            if "NotFound" in stderr or "not found" in stderr.lower():
                logging(f"Chart app {release_name} not found; "
                        f"nothing to uninstall")
                return {}
            raise Exception(
                f"Failed to uninstall chart {release_name}: {stderr[:500]}"
            )

        logging(f"Chart uninstall initiated: {release_name}")
        return json.loads(stdout) if stdout.strip() else {}

    def wait_for_chart_app_deleted(self, cluster_id, release_name, namespace,
                                   timeout=DEFAULT_TIMEOUT):
        """Wait for a chart app to be fully removed from a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name
            namespace: Namespace where the chart was installed
            timeout: Timeout in seconds
        """
        logging(f"Waiting for chart app {release_name} to be deleted "
                f"from {namespace}")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            rc, stdout, stderr = self._run_kubectl_guest(
                cluster_id,
                ["get", "apps.catalog.cattle.io", release_name,
                 "-n", namespace, "-o", "json"]
            )
            if rc != 0 and ("NotFound" in stderr or
                            "not found" in stderr.lower()):
                logging(f"Chart app {release_name} has been deleted")
                return True
            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for chart app {release_name} to be deleted"
        )

    def create_cluster_repo(self, cluster_id, repo_name, git_url, git_branch):
        """Create a ClusterRepo on a guest cluster via kubectl.

        Args:
            cluster_id: Rancher management cluster ID
            repo_name: Name for the ClusterRepo resource
            git_url: Git repository URL
            git_branch: Git branch name
        """
        logging(f"Creating ClusterRepo {repo_name} on cluster {cluster_id} "
                f"(branch={git_branch})")

        repo_spec = {
            "apiVersion": "catalog.cattle.io/v1",
            "kind": "ClusterRepo",
            "metadata": {
                "name": repo_name
            },
            "spec": {
                "gitRepo": git_url,
                "gitBranch": git_branch
            }
        }

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(repo_spec)
        )

        if rc != 0:
            if "AlreadyExists" in stderr:
                logging(f"ClusterRepo {repo_name} already exists")
                return repo_spec
            raise Exception(
                f"Failed to create ClusterRepo {repo_name}: {stderr[:500]}"
            )

        logging(f"Created ClusterRepo {repo_name}")
        return repo_spec

    def wait_for_cluster_repo_ready(self, cluster_id, repo_name,
                                    timeout=DEFAULT_TIMEOUT,
                                    expected_git_branch=None):
        """Wait for a ClusterRepo to finish downloading on a guest cluster.

        Requires Downloaded=True on two consecutive polls to avoid the
        false-positive that occurs briefly after initial creation before
        the controller begins the real download.

        Args:
            cluster_id: Rancher management cluster ID
            repo_name: ClusterRepo name
            timeout: Maximum wait time in seconds
            expected_git_branch: When set, verify spec.gitBranch matches
        """
        logging(f"Waiting for ClusterRepo {repo_name} to be ready"
                + (f" (branch={expected_git_branch})"
                   if expected_git_branch else ""))
        end_time = time.time() + int(timeout)
        consecutive_ready = 0
        reapply_count = 0
        max_reapply = 3

        while time.time() < end_time:
            rc, stdout, stderr = self._run_kubectl_guest(
                cluster_id,
                ["get", "clusterrepos.catalog.cattle.io", repo_name,
                 "-o", "json"]
            )
            downloaded = False
            data = {}
            if rc == 0 and stdout.strip():
                data = json.loads(stdout)
                conditions = data.get("status", {}).get("conditions", [])
                for c in conditions:
                    if (c.get("type") == "Downloaded" and
                            c.get("status") == "True"):
                        downloaded = True
                        break

            if downloaded:
                consecutive_ready += 1
                if consecutive_ready >= 2:
                    # Verify branch matches expected value if provided
                    if expected_git_branch:
                        spec = data.get("spec", {})
                        current_branch = spec.get("gitBranch", "")
                        current_url = spec.get("gitRepo", "")

                        if current_branch != expected_git_branch:
                            if reapply_count >= max_reapply:
                                raise Exception(
                                    f"ClusterRepo {repo_name} branch mismatch "
                                    f"after {max_reapply} re-apply attempts. "
                                    f"Expected branch={expected_git_branch}, "
                                    f"got branch={current_branch}. "
                                    f"Rancher is reconciling the repo back to "
                                    f"its built-in defaults."
                                )
                            logging(
                                f"ClusterRepo {repo_name} branch mismatch "
                                f"(got={current_branch}, "
                                f"expected={expected_git_branch}), "
                                f"re-applying "
                                f"(attempt {reapply_count + 1}/{max_reapply})",
                                level="WARNING"
                            )
                            repo_spec = {
                                "apiVersion": "catalog.cattle.io/v1",
                                "kind": "ClusterRepo",
                                "metadata": {"name": repo_name},
                                "spec": {
                                    "gitRepo": current_url,
                                    "gitBranch": expected_git_branch
                                }
                            }
                            self._run_kubectl_guest(
                                cluster_id,
                                ["apply", "-f", "-"],
                                input_data=json.dumps(repo_spec)
                            )
                            reapply_count += 1
                            consecutive_ready = 0
                            time.sleep(5)
                            continue

                    logging(f"ClusterRepo {repo_name} is ready "
                            f"(stable, spec verified)")
                    return data
            else:
                consecutive_ready = 0

            time.sleep(5)

        raise Exception(
            f"ClusterRepo {repo_name} not ready after {timeout}s"
        )

    def get_chart_versions(self, repo_name, chart_name, cluster_id=None):
        """Get available versions for a chart from a Rancher chart repo.

        When cluster_id is given, retries up to 60s for the chart index
        to be populated (it can lag behind the Downloaded condition).

        Args:
            repo_name: Chart repository name
            chart_name: Chart name
            cluster_id: Optional guest cluster ID; when provided, queries
                        the guest cluster repo via kubectl

        Returns:
            list: Available version strings, newest first
        """
        logging(f"Getting versions for chart {chart_name} from {repo_name}")

        max_attempts = 12 if cluster_id else 1
        for attempt in range(max_attempts):
            if cluster_id:
                rc, stdout, stderr = self._run_kubectl_raw_guest(
                    cluster_id,
                    f"/v1/catalog.cattle.io.clusterrepos/{repo_name}"
                    f"?link=index"
                )
            else:
                rc, stdout, stderr = self._run_kubectl_rancher(
                    ["get", "--raw",
                     f"/v1/catalog.cattle.io.clusterrepos/{repo_name}"
                     f"?link=index"]
                )

            if rc != 0:
                if attempt < max_attempts - 1:
                    logging(f"Chart index not available yet, retrying "
                            f"({attempt + 1}/{max_attempts})...")
                    time.sleep(5)
                    continue
                raise Exception(
                    f"Failed to get chart index: {stderr}"
                )

            data = json.loads(stdout)
            entries = data.get("entries", {})
            chart_entries = entries.get(chart_name, [])
            versions = [e.get("version", "") for e in chart_entries]

            if versions or attempt >= max_attempts - 1:
                logging(f"Found {len(versions)} versions for {chart_name}: "
                        f"{versions[:5]}")
                return versions

            logging(f"Chart {chart_name} not in index yet, retrying "
                    f"({attempt + 1}/{max_attempts})...")
            time.sleep(5)

        return []

    def get_deployed_chart_version(self, cluster_id, release_name, namespace):
        """Return the deployed version string of an installed chart app.

        Reads the version from apps.catalog.cattle.io on the guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name (e.g. harvester-csi-driver)
            namespace: Namespace where the chart was installed

        Returns:
            str: Deployed chart version (e.g. '0.1.18')
        """
        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["get", "apps.catalog.cattle.io", release_name,
             "-n", namespace, "-o", "json"]
        )
        if rc != 0:
            raise Exception(
                f"Failed to get chart app {release_name}: {stderr}")
        data = json.loads(stdout)
        version = (data.get("spec", {})
                       .get("chart", {})
                       .get("metadata", {})
                       .get("version", "unknown"))
        logging(f"Deployed chart version for {release_name}: {version}")
        return version

    def create_cloud_config_secret(self, cluster_id, secret_name,
                                   namespace, kubeconfig):
        """Create a cloud-provider-config secret on a guest cluster.

        Args:
            cluster_id: Rancher management cluster ID
            secret_name: Name for the secret
            namespace: Target namespace (typically kube-system)
            kubeconfig: The Harvester cloud-provider kubeconfig content

        Returns:
            dict: Created secret spec
        """
        logging(f"Creating cloud config secret {secret_name} in "
                f"{namespace} on cluster {cluster_id}")

        import base64
        encoded = base64.b64encode(
            kubeconfig.encode("utf-8")
        ).decode("utf-8")

        secret_spec = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {
                "name": secret_name,
                "namespace": namespace
            },
            "type": "Opaque",
            "data": {
                "cloud-provider-config": encoded
            }
        }

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(secret_spec)
        )

        if rc != 0:
            if "AlreadyExists" in stderr:
                logging(f"Secret {secret_name} already exists")
                return secret_spec
            raise Exception(
                f"Failed to create cloud config secret: {stderr[:300]}"
            )

        logging(f"Created cloud config secret: {secret_name}")
        return secret_spec

    def write_cloud_config_to_nodes(self, cluster_id, secret_name, namespace):
        """Deploy a temporary privileged DaemonSet on the guest cluster that
        copies the cloud-provider-config secret data to the node hostPath
        (/var/lib/rancher/rke2/etc/config-files/cloud-provider-config).
        The DaemonSet is deleted after all nodes are configured.

        Args:
            cluster_id: Rancher management cluster ID
            secret_name: Name of the secret containing 'cloud-provider-config'
            namespace: Namespace of the secret (typically kube-system)
        """
        ds_name = "harvester-cp-config-writer"
        target_path = (
            "/var/lib/rancher/rke2/etc/config-files/cloud-provider-config"
        )
        ds_spec = {
            "apiVersion": "apps/v1",
            "kind": "DaemonSet",
            "metadata": {"name": ds_name, "namespace": namespace},
            "spec": {
                "selector": {
                    "matchLabels": {"app": ds_name}
                },
                "template": {
                    "metadata": {"labels": {"app": ds_name}},
                    "spec": {
                        "tolerations": [{"operator": "Exists"}],
                        "initContainers": [{
                            "name": "write-config",
                            "image": "busybox:1.36.1",
                            "securityContext": {"privileged": True},
                            "command": [
                                "sh", "-c",
                                (
                                    "mkdir -p /host/var/lib/rancher/rke2/etc/"
                                    "config-files && "
                                    "cp /config/cloud-provider-config "
                                    "/host" + target_path + " && "
                                    "chmod 0600 /host" + target_path
                                )
                            ],
                            "volumeMounts": [
                                {"name": "config", "mountPath": "/config"},
                                {"name": "host-root", "mountPath": "/host"}
                            ]
                        }],
                        "containers": [{
                            "name": "pause",
                            "image": "rancher/mirrored-pause:3.7"
                        }],
                        "volumes": [
                            {
                                "name": "config",
                                "secret": {
                                    "secretName": secret_name,
                                    "items": [{
                                        "key": "cloud-provider-config",
                                        "path": "cloud-provider-config"
                                    }]
                                }
                            },
                            {
                                "name": "host-root",
                                "hostPath": {"path": "/"}
                            }
                        ]
                    }
                }
            }
        }

        logging(f"Deploying DaemonSet {ds_name} to write cloud config on "
                f"cluster {cluster_id}")
        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(ds_spec)
        )
        if rc != 0:
            raise Exception(
                f"Failed to deploy cloud-config writer DaemonSet: {stderr[:300]}"
            )

        # Wait until all scheduled nodes have the initContainer completed
        import time as _time
        end_time = _time.time() + 300
        while _time.time() < end_time:
            rc, stdout, stderr = self._run_kubectl_guest(
                cluster_id,
                ["get", "daemonset", ds_name, "-n", namespace,
                 "-o", "jsonpath={.status.desiredNumberScheduled}"
                 "/{.status.numberReady}"]
            )
            if rc == 0 and "/" in stdout:
                desired, ready = stdout.strip().split("/")
                if desired == ready and int(desired) > 0:
                    logging(f"All {ready}/{desired} nodes configured")
                    break
            _time.sleep(10)
        else:
            raise Exception(
                "Timed out waiting for cloud-config writer DaemonSet to finish"
            )

        # Clean up the DaemonSet
        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "daemonset", ds_name, "-n", namespace,
             "--ignore-not-found"]
        )
        if rc != 0:
            logging(f"Warning: failed to delete DaemonSet {ds_name}: {stderr}")
        else:
            logging(f"Deleted DaemonSet {ds_name}")

    def wait_for_chart_app_ready(self, cluster_id, release_name,
                                 namespace, timeout=DEFAULT_TIMEOUT,
                                 expected_version=None):
        """Wait for a chart app to be deployed and ready.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name
            namespace: Namespace where the chart was installed
            timeout: Timeout in seconds
        """
        logging(f"Waiting for chart app {release_name} to be ready "
                f"in {namespace}"
                + (f" at version {expected_version}" if expected_version else ""))
        retry_count, retry_interval = get_retry_count_and_interval()

        # Give the async helm operation pod time to start so the status
        # transitions away from the stale pre-upgrade 'deployed' state.
        if expected_version:
            time.sleep(5)

        end_time = time.time() + int(timeout)
        iteration = 0
        while time.time() < end_time:
            try:
                rc, stdout, stderr = self._run_kubectl_guest(
                    cluster_id,
                    ["get", "apps.catalog.cattle.io", release_name,
                     "-n", namespace, "-o", "json"]
                )
                if rc == 0 and stdout.strip():
                    data = json.loads(stdout)
                    info = data.get("spec", {}).get("info", {})
                    status = info.get("status", "")
                    version = (data.get("spec", {})
                                   .get("chart", {})
                                   .get("metadata", {})
                                   .get("version", ""))

                    if expected_version:
                        if status == "deployed" and version == expected_version:
                            logging(f"Chart app {release_name} deployed "
                                    f"at {expected_version}")
                            return data
                    else:
                        if status == "deployed":
                            logging(f"Chart app {release_name} is deployed")
                            return data

                    if iteration % 10 == 0:
                        logging(f"Chart app {release_name} status: "
                                f"{status}, version: {version}")
                elif iteration % 10 == 0:
                    logging(f"Chart app {release_name} not found yet: "
                            f"{stderr.strip()[:200]}", level="WARNING")
            except Exception as e:
                if iteration % 10 == 0:
                    logging(f"Error checking chart app: {e}",
                            level="WARNING")

            iteration += 1
            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for chart app {release_name} to be ready"
            + (f" at version {expected_version}" if expected_version else "")
        )

    # RWX Volume / StorageClass / StatefulSet Operations
    def create_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Create an RWX-capable StorageClass on the host Harvester cluster"""
        logging(f"Creating RWX StorageClass on host: {name}")

        sc_spec = {
            "apiVersion": "storage.k8s.io/v1",
            "kind": "StorageClass",
            "metadata": {
                "name": name
            },
            "provisioner": "driver.longhorn.io",
            "allowVolumeExpansion": True,
            "reclaimPolicy": "Delete",
            "volumeBindingMode": "Immediate",
            "parameters": {
                "numberOfReplicas": "3",
                "staleReplicaTimeout": "2880",
                "fromBackup": "",
                "fsType": "ext4",
                "nfsOptions": "vers=4.2,noresvport,softerr,timeo=600,retrans=5"
            }
        }

        rc, stdout, stderr = self._run_kubectl(
            ["apply", "-f", "-"],
            input_data=json.dumps(sc_spec)
        )

        if rc != 0:
            if "AlreadyExists" not in stderr:
                raise Exception(
                    f"Failed to create RWX StorageClass on host: {stderr}"
                )

        logging(f"Created RWX StorageClass on host: {name}")
        return sc_spec

    def delete_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Delete the RWX StorageClass from the host Harvester cluster"""
        logging(f"Deleting RWX StorageClass from host: {name}")

        rc, stdout, stderr = self._run_kubectl(
            ["delete", "storageclass", name, "--ignore-not-found"]
        )

        if rc != 0:
            raise Exception(
                f"Failed to delete RWX StorageClass from host: {stderr}"
            )

        logging(f"Deleted RWX StorageClass from host: {name}")

    def create_guest_storage_class(self, cluster_id, name,
                                   host_storage_class="longhorn-rwx"):
        """Create a StorageClass on the guest cluster referencing a host SC"""
        logging(f"Creating guest StorageClass {name} in cluster {cluster_id} "
                f"(hostStorageClass={host_storage_class})")

        sc_spec = {
            "apiVersion": "storage.k8s.io/v1",
            "kind": "StorageClass",
            "metadata": {
                "name": name
            },
            "provisioner": "driver.harvesterhci.io",
            "allowVolumeExpansion": True,
            "reclaimPolicy": "Delete",
            "volumeBindingMode": "Immediate",
            "parameters": {
                "hostStorageClass": host_storage_class
            }
        }

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(sc_spec)
        )

        if rc != 0:
            if "AlreadyExists" not in stderr:
                raise Exception(
                    f"Failed to create guest StorageClass: {stderr}"
                )

        logging(f"Created guest StorageClass: {name}")
        return sc_spec

    def delete_guest_storage_class(self, cluster_id, name):
        """Delete a StorageClass from the guest cluster"""
        logging(f"Deleting guest StorageClass {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "storageclass", name, "--ignore-not-found"]
        )

        if rc != 0:
            raise Exception(
                f"Failed to delete guest StorageClass: {stderr}"
            )

        logging(f"Deleted guest StorageClass: {name}")

    def create_pvc_rwx(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create a ReadWriteMany PVC in guest cluster via kubectl"""
        logging(f"Creating RWX PVC {name} in cluster {cluster_id}")

        pvc_spec = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": {
                "name": name,
                "namespace": DEFAULT_NAMESPACE
            },
            "spec": {
                "accessModes": ["ReadWriteMany"],
                "resources": {
                    "requests": {
                        "storage": size
                    }
                }
            }
        }
        if storage_class:
            pvc_spec["spec"]["storageClassName"] = storage_class

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(pvc_spec)
        )
        if rc != 0:
            raise Exception(f"Failed to create RWX PVC: {stderr}")

        logging(f"Created RWX PVC {name}")
        return pvc_spec

    def create_statefulset(self, cluster_id, namespace, name, image,
                           pvc_name, replicas=2):
        """Create a StatefulSet that mounts an existing PVC via kubectl"""
        logging(f"Creating StatefulSet {name} in cluster {cluster_id}")

        container = {
            "name": name,
            "image": image,
            "command": ["sh", "-c", "sleep infinity"],
        }

        container["volumeMounts"] = [
            {"name": "shared-data", "mountPath": "/data"}
        ]

        sts_spec = {
            "apiVersion": "apps/v1",
            "kind": "StatefulSet",
            "metadata": {
                "name": name,
                "namespace": namespace
            },
            "spec": {
                "replicas": int(replicas),
                "serviceName": name,
                "selector": {
                    "matchLabels": {
                        "name": name
                    }
                },
                "template": {
                    "metadata": {
                        "labels": {
                            "name": name
                        }
                    },
                    "spec": {
                        "containers": [container],
                        "volumes": [
                            {
                                "name": "shared-data",
                                "persistentVolumeClaim": {
                                    "claimName": pvc_name
                                }
                            }
                        ]
                    }
                }
            }
        }

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["apply", "-f", "-"],
            input_data=json.dumps(sts_spec)
        )
        if rc != 0:
            raise Exception(f"Failed to create StatefulSet: {stderr}")

        logging(f"Created StatefulSet {name}")
        return sts_spec

    def get_statefulset(self, cluster_id, namespace, name):
        """Get StatefulSet details from guest cluster via kubectl"""
        logging(f"Getting StatefulSet {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["get", "statefulset", name, "-n", namespace, "-o", "json"]
        )

        if rc != 0:
            if "NotFound" in stderr or "not found" in stderr.lower():
                return None
            raise Exception(f"Failed to get StatefulSet: {stderr}")

        return json.loads(stdout)

    def delete_statefulset(self, cluster_id, namespace, name):
        """Delete StatefulSet from guest cluster via kubectl"""
        logging(f"Deleting StatefulSet {name} from cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "statefulset", name, "-n", namespace,
             "--ignore-not-found"]
        )

        if rc != 0:
            raise Exception(f"Failed to delete StatefulSet: {stderr}")

        logging(f"Deleted StatefulSet {name}")

    def wait_for_statefulset_ready(self, cluster_id, namespace, name,
                                   timeout=DEFAULT_TIMEOUT):
        """Wait for StatefulSet to have all replicas ready"""
        logging(f"Waiting for StatefulSet {name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            try:
                sts = self.get_statefulset(cluster_id, namespace, name)
                if sts:
                    status = sts.get("status", {})
                    replicas = sts.get("spec", {}).get("replicas", 0)
                    ready = status.get("readyReplicas", 0)
                    if replicas > 0 and ready >= replicas:
                        logging(f"StatefulSet {name} is ready "
                                f"({ready}/{replicas})")
                        return sts
            except Exception as e:
                logging(f"Error checking StatefulSet status: {e}",
                        level="WARNING")

            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for StatefulSet {name} to be ready"
        )

    def exec_command_in_pod(self, cluster_id, namespace, pod_name, command):
        """Execute a command inside a pod via kubectl exec"""
        logging(f"Executing command in pod {pod_name}: {command}")

        args = ["exec", pod_name, "-n", namespace, "--"]
        args.extend(command)

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id, args
        )

        if rc != 0:
            raise Exception(
                f"Failed to exec in pod {pod_name}: {stderr}"
            )

        return stdout.strip()

    def get_pods_by_label(self, cluster_id, namespace, label_selector):
        """Get pods matching a label selector via kubectl"""
        logging(f"Getting pods with label {label_selector} in "
                f"cluster {cluster_id}")

        rc, stdout, stderr = self._run_kubectl_guest(
            cluster_id,
            ["get", "pods", "-n", namespace,
             "-l", label_selector, "-o", "json"]
        )

        if rc != 0:
            raise Exception(f"Failed to get pods: {stderr}")

        data = json.loads(stdout)
        items = data.get("items", [])
        logging(f"Found {len(items)} pods with label {label_selector}")
        return items

    # Rancher RBAC Operations

    def _run_helm(self, args, kubeconfig=None):
        """Run a helm command against Rancher's local cluster.

        Args:
            args: List of helm sub-command arguments
            kubeconfig: Path to kubeconfig file (uses Rancher kubeconfig by default)

        Returns:
            tuple: (return_code, stdout, stderr)
        """
        cmd = ["helm"]
        if kubeconfig:
            cmd.extend(["--kubeconfig", kubeconfig])
        cmd.extend(args)
        logging(f"Running helm: {' '.join(cmd)}", level="DEBUG")
        result = subprocess.run(cmd, capture_output=True, text=True)  # nosec B603
        return result.returncode, result.stdout, result.stderr

    def create_rancher_user(self, user_id, display_name):
        """Create a Rancher local user via kubectl against Rancher's K8s API.

        Args:
            user_id: Username (used as both metadata.name and username field)
            display_name: Human-readable display name

        Returns:
            dict: Created user resource
        """
        logging(f"Creating Rancher user: {user_id}")

        manifest = {
            "apiVersion": "management.cattle.io/v3",
            "kind": "User",
            "metadata": {"name": user_id},
            "displayName": display_name,
            "username": user_id,
        }

        yaml_str = yaml.dump(manifest)
        rc, stdout, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"], input_data=yaml_str
        )

        if rc != 0:
            raise Exception(f"Failed to create Rancher user '{user_id}': {stderr}")

        logging(f"Created Rancher user: {user_id}")
        return manifest

    def delete_rancher_user(self, user_id):
        """Delete a Rancher user and its associated password secret.

        Args:
            user_id: Username to delete
        """
        logging(f"Deleting Rancher user: {user_id}")

        rc, _, stderr = self._run_kubectl_rancher([
            "delete", "user.management.cattle.io", user_id,
            "--ignore-not-found"
        ])
        if rc != 0:
            logging(f"Warning: failed to delete user '{user_id}': {stderr}", level="WARNING")

        rc, _, stderr = self._run_kubectl_rancher([
            "delete", "secret", user_id,
            "-n", "cattle-local-user-passwords",
            "--ignore-not-found"
        ])
        if rc != 0:
            logging(
                f"Warning: failed to delete password secret for '{user_id}': {stderr}",
                level="WARNING"
            )

        logging(f"Deleted Rancher user: {user_id}")

    def set_user_password(self, user_id, password):
        """Create or update the password Secret for a Rancher local user.

        The secret is created in the cattle-local-user-passwords namespace.

        Args:
            user_id: Username whose password to set
            password: Plaintext password value
        """
        logging(f"Setting password for Rancher user: {user_id}")

        manifest = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {
                "name": user_id,
                "namespace": "cattle-local-user-passwords",
            },
            "type": "Opaque",
            "stringData": {"password": password},
        }

        yaml_str = yaml.dump(manifest)
        rc, _, stderr = self._run_kubectl_rancher(
            ["apply", "-f", "-"], input_data=yaml_str
        )

        if rc != 0:
            raise Exception(
                f"Failed to set password for user '{user_id}': {stderr}"
            )

        logging(f"Password set for user: {user_id}")

    def assign_standard_user_role(self, user_id):
        """Assign the Standard User global role via a GlobalRoleBinding.

        Creates a GlobalRoleBinding with globalRoleName: user which grants the
        built-in 'Standard User' role.

        Args:
            user_id: Username to assign the role to
        """
        logging(f"Assigning Standard User global role to: {user_id}")

        manifest = {
            "apiVersion": "management.cattle.io/v3",
            "kind": "GlobalRoleBinding",
            "metadata": {"generateName": "grb-"},
            "globalRoleName": "user",
            "userName": user_id,
            "userPrincipalName": user_id,
        }

        yaml_str = yaml.dump(manifest)
        rc, _, stderr = self._run_kubectl_rancher(
            ["create", "-f", "-"], input_data=yaml_str
        )

        if rc != 0:
            raise Exception(
                f"Failed to assign Standard User role to '{user_id}': {stderr}"
            )

        logging(f"Assigned Standard User role to: {user_id}")

    def create_project(self, cluster_id, display_name):
        """Create a new Rancher project in the given cluster.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            display_name: Display name for the project

        Returns:
            str: Short project ID (e.g. p-xxxxx)
        """
        logging(f"Creating project '{display_name}' in cluster {cluster_id}")

        manifest = {
            "apiVersion": "management.cattle.io/v3",
            "kind": "Project",
            "metadata": {
                "generateName": "p-",
                "namespace": cluster_id,
            },
            "spec": {
                "clusterName": cluster_id,
                "displayName": display_name,
                "containerDefaultResourceLimit": {},
                "namespaceDefaultResourceQuota": {"limit": {}},
                "resourceQuota": {"limit": {}, "usedLimit": {}},
            },
        }

        yaml_str = yaml.dump(manifest)
        rc, stdout, stderr = self._run_kubectl_rancher(
            ["create", "-f", "-", "-o", "json"], input_data=yaml_str
        )

        if rc != 0:
            raise Exception(
                f"Failed to create project '{display_name}' in cluster '{cluster_id}': {stderr}"
            )

        data = json.loads(stdout)
        project_id = data.get("metadata", {}).get("name", "")
        logging(f"Created project '{display_name}': {project_id}")
        return project_id

    def delete_project(self, cluster_id, project_id):
        """Delete a Rancher project.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        logging(f"Deleting project {project_id} in cluster {cluster_id}")

        rc, _, stderr = self._run_kubectl_rancher([
            "delete", "projects.management.cattle.io",
            "-n", cluster_id, project_id, "--ignore-not-found"
        ])

        if rc != 0:
            raise Exception(
                f"Failed to delete project '{project_id}' in cluster '{cluster_id}': {stderr}"
            )

        logging(f"Deleted project {project_id}")

    def assign_project_role(self, user_id, cluster_id, project_id, role_template_name):
        """Create a ProjectRoleTemplateBinding to grant a user a project-scoped role.

        The PRTB namespace follows Rancher's convention:
        {cluster_id}-{project_id} (e.g. c-m-xxxxx-p-yyyyy).

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-project-view)
        """
        prtb_namespace = f"{cluster_id}-{project_id}"
        project_name = f"{cluster_id}:{project_id}"

        logging(
            f"Assigning role '{role_template_name}' to user '{user_id}' "
            f"in project '{project_name}'"
        )

        manifest = {
            "apiVersion": "management.cattle.io/v3",
            "kind": "ProjectRoleTemplateBinding",
            "metadata": {
                "generateName": "prtb-",
                "namespace": prtb_namespace,
            },
            "projectName": project_name,
            "roleTemplateName": role_template_name,
            "userName": user_id,
            "userPrincipalName": f"local://{user_id}",
        }

        yaml_str = yaml.dump(manifest)
        rc, _, stderr = self._run_kubectl_rancher(
            ["create", "-f", "-"], input_data=yaml_str
        )

        if rc != 0:
            raise Exception(
                f"Failed to assign project role '{role_template_name}' "
                f"to '{user_id}': {stderr}"
            )

        logging(
            f"Assigned project role '{role_template_name}' to user '{user_id}'"
        )

    def delete_user_global_role_bindings(self, user_id):
        """Delete all GlobalRoleBindings owned by the given user.

        Args:
            user_id: Username whose GlobalRoleBindings to remove
        """
        logging(f"Deleting GlobalRoleBindings for user: {user_id}")

        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "globalrolebindings.management.cattle.io",
            "-o", "json"
        ])

        if rc != 0:
            logging(
                f"Warning: failed to list GlobalRoleBindings: {stderr}",
                level="WARNING"
            )
            return

        data = json.loads(stdout)
        deleted = 0
        for item in data.get("items", []):
            if item.get("userName") == user_id:
                name = item.get("metadata", {}).get("name", "")
                rc, _, stderr = self._run_kubectl_rancher([
                    "delete", "globalrolebinding.management.cattle.io",
                    name, "--ignore-not-found"
                ])
                if rc != 0:
                    logging(
                        f"Warning: failed to delete GRB '{name}': {stderr}",
                        level="WARNING"
                    )
                else:
                    deleted += 1

        logging(f"Deleted {deleted} GlobalRoleBinding(s) for user '{user_id}'")

    def delete_user_project_role_bindings(self, user_id, cluster_id, project_id):
        """Delete all ProjectRoleTemplateBindings owned by the user in a project.

        Args:
            user_id: Username whose bindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        prtb_namespace = f"{cluster_id}-{project_id}"
        logging(
            f"Deleting ProjectRoleTemplateBindings for user '{user_id}' "
            f"in namespace '{prtb_namespace}'"
        )

        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "projectroletemplatebindings.management.cattle.io",
            "-n", prtb_namespace, "-o", "json"
        ])

        if rc != 0:
            logging(
                f"Warning: failed to list PRTBs in '{prtb_namespace}': {stderr}",
                level="WARNING"
            )
            return

        data = json.loads(stdout)
        deleted = 0
        for item in data.get("items", []):
            if item.get("userName") == user_id:
                name = item.get("metadata", {}).get("name", "")
                rc, _, stderr = self._run_kubectl_rancher([
                    "delete", "projectroletemplatebinding.management.cattle.io",
                    name, "-n", prtb_namespace, "--ignore-not-found"
                ])
                if rc != 0:
                    logging(
                        f"Warning: failed to delete PRTB '{name}': {stderr}",
                        level="WARNING"
                    )
                else:
                    deleted += 1

        logging(
            f"Deleted {deleted} ProjectRoleTemplateBinding(s) for user '{user_id}'"
        )

    def assign_cluster_role(self, user_id, cluster_id, role_template_name):
        """Create a ClusterRoleTemplateBinding for a cluster-scoped role.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-cluster-view)
        """
        logging(
            f"Assigning cluster role '{role_template_name}' to user '{user_id}' "
            f"in cluster '{cluster_id}'"
        )

        manifest = {
            "apiVersion": "management.cattle.io/v3",
            "kind": "ClusterRoleTemplateBinding",
            "metadata": {
                "generateName": "crtb-",
                "namespace": cluster_id,
            },
            "clusterName": cluster_id,
            "roleTemplateName": role_template_name,
            "userName": user_id,
            "userPrincipalName": f"local://{user_id}",
        }

        yaml_str = yaml.dump(manifest)
        rc, _, stderr = self._run_kubectl_rancher(
            ["create", "-f", "-"], input_data=yaml_str
        )

        if rc != 0:
            raise Exception(
                f"Failed to assign cluster role '{role_template_name}' "
                f"to '{user_id}': {stderr}"
            )

        logging(
            f"Assigned cluster role '{role_template_name}' to user '{user_id}'"
        )

    def delete_user_cluster_role_bindings(self, user_id, cluster_id):
        """Delete all ClusterRoleTemplateBindings for the given user in a cluster.

        Args:
            user_id: Username whose ClusterRoleTemplateBindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(
            f"Deleting ClusterRoleTemplateBindings for user '{user_id}' "
            f"in cluster '{cluster_id}'"
        )

        rc, stdout, stderr = self._run_kubectl_rancher([
            "get", "clusterroletemplatebindings.management.cattle.io",
            "-n", cluster_id, "-o", "json"
        ])

        if rc != 0:
            logging(
                f"Warning: failed to list CRTBs in '{cluster_id}': {stderr}",
                level="WARNING"
            )
            return

        data = json.loads(stdout)
        deleted = 0
        for item in data.get("items", []):
            if item.get("userName") == user_id:
                name = item.get("metadata", {}).get("name", "")
                rc, _, stderr = self._run_kubectl_rancher([
                    "delete", "clusterroletemplatebinding.management.cattle.io",
                    name, "-n", cluster_id, "--ignore-not-found"
                ])
                if rc != 0:
                    logging(
                        f"Warning: failed to delete CRTB '{name}': {stderr}",
                        level="WARNING"
                    )
                else:
                    deleted += 1

        logging(
            f"Deleted {deleted} ClusterRoleTemplateBinding(s) for user '{user_id}'"
        )

    def _run_kubectl_with_content(self, kubeconfig_content, args):
        """Run kubectl using an in-memory kubeconfig string."""
        fd, path = tempfile.mkstemp(suffix=".kubeconfig")
        try:
            with os.fdopen(fd, 'w') as f:
                f.write(kubeconfig_content)
            return self._run_kubectl(args, kubeconfig=path, insecure=True)
        finally:
            if os.path.exists(path):
                os.remove(path)

    def generate_user_kubeconfig(self, user_id, password, cluster_id):
        """Login as user_id and return a kubeconfig YAML string for cluster_id."""
        endpoint, _, _ = self._get_rancher_credentials()
        logging(f"Generating kubeconfig for user '{user_id}' on cluster '{cluster_id}'")

        from urllib.parse import urljoin
        session = requests.Session()
        session.verify = False
        session.headers.update({"Content-Type": "application/json"})

        # Step 1: authenticate as the test user
        auth_resp = session.post(
            urljoin(endpoint, "v3-public/localProviders/local?action=login"),
            json={"username": user_id, "password": password}
        )
        if auth_resp.status_code != 201:
            raise Exception(
                f"Failed to authenticate as '{user_id}': "
                f"{auth_resp.status_code} {auth_resp.text}"
            )
        user_token = auth_resp.json().get("token")
        if not user_token:
            raise Exception(f"No token returned for '{user_id}'")

        # Step 2: generate kubeconfig for the cluster
        kc_resp = session.post(
            f"{endpoint.rstrip('/')}/v3/clusters/{cluster_id}"
            f"?action=generateKubeconfig",
            headers={"Authorization": f"Bearer {user_token}"}
        )
        if kc_resp.status_code != 200:
            raise Exception(
                f"Failed to generate kubeconfig for '{user_id}': "
                f"{kc_resp.status_code} {kc_resp.text}"
            )
        kubeconfig = kc_resp.json().get("config")
        if not kubeconfig:
            raise Exception(f"Empty kubeconfig returned for '{user_id}'")

        logging(f"Generated kubeconfig for user '{user_id}'")
        return kubeconfig

    def verify_resource_access(self, kubeconfig_content, verb, resource, namespace):
        """Run kubectl auth can-i <verb> <resource> -n <namespace> and return (ok, output)."""
        rc, stdout, stderr = self._run_kubectl_with_content(
            kubeconfig_content,
            ["auth", "can-i", verb, resource, "-n", namespace]
        )
        output = stdout.strip() if stdout else stderr.strip()
        logging(
            f"kubectl auth can-i {verb} {resource} -n {namespace}: rc={rc}, "
            f"output={output[:300]}",
            level="DEBUG"
        )
        return rc == 0, output

    def create_namespace_in_project(self, namespace_name, cluster_id, project_id):
        """Create namespace in Harvester cluster and bind it to a Rancher project."""
        project_ref = f"{cluster_id}:{project_id}"
        logging(f"Creating namespace '{namespace_name}' in project '{project_ref}'")

        manifest = {
            "apiVersion": "v1",
            "kind": "Namespace",
            "metadata": {
                "name": namespace_name,
                "annotations": {
                    "field.cattle.io/projectId": project_ref
                }
            }
        }
        yaml_str = yaml.dump(manifest)
        rc, _, stderr = self._run_kubectl_guest(
            cluster_id, ["apply", "-f", "-"], input_data=yaml_str
        )
        if rc != 0:
            raise Exception(
                f"Failed to create namespace '{namespace_name}': {stderr}"
            )
        logging(f"Namespace '{namespace_name}' created and assigned to project")

    def delete_namespace_from_cluster(self, namespace_name, cluster_id):
        """Delete a namespace from the Harvester cluster."""
        logging(f"Deleting namespace '{namespace_name}' from cluster '{cluster_id}'")
        rc, _, stderr = self._run_kubectl_guest(
            cluster_id,
            ["delete", "namespace", namespace_name, "--ignore-not-found"]
        )
        if rc != 0:
            logging(
                f"Warning: failed to delete namespace '{namespace_name}': {stderr}",
                level="WARNING"
            )
