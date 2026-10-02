"""
Rancher REST Implementation - Harvester and Rancher REST API operations
Layer 4: Makes actual REST API calls for Rancher integration operations
"""

import os
import time
import requests
import json
import re
from urllib.parse import urlsplit, urlunsplit
from utility.utility import logging, get_retry_count_and_interval, get_harvester_api_client
from constant import DEFAULT_TIMEOUT, DEFAULT_TIMEOUT_LONG, DEFAULT_NAMESPACE
from rancher.base import Base


class Rest(Base):
    """
    REST implementation for Rancher Integration operations using REST APIs

    This implementation uses both Harvester and Rancher REST APIs.
    """

    def __init__(self):
        """Initialize REST clients"""
        self.harvester_api = get_harvester_api_client()

        # Rancher API client (initialize on-demand)
        self.rancher_endpoint = self._normalize_endpoint(
            os.getenv("RANCHER_ENDPOINT", "")
        )
        self.rancher_token = None
        self.rancher_session = None

    @staticmethod
    def _normalize_endpoint(endpoint):
        parsed_endpoint = urlsplit(endpoint)
        if parsed_endpoint.scheme == "http":
            return urlunsplit(("https", parsed_endpoint.netloc,
                               parsed_endpoint.path, parsed_endpoint.query,
                               parsed_endpoint.fragment))
        return endpoint

    def _authenticate_rancher(self, rancher_endpoint=None):
        """Authenticate with Rancher and get token"""
        if self.rancher_token and self.rancher_session:
            return  # Already authenticated

        if not rancher_endpoint:
            rancher_endpoint = self.rancher_endpoint
        rancher_endpoint = self._normalize_endpoint(rancher_endpoint)
        self.rancher_endpoint = rancher_endpoint

        # API keys authenticate directly and do not require the login endpoint.
        try:
            from robot.libraries.BuiltIn import BuiltIn
            api_key = BuiltIn().get_variable_value("${RANCHER_API_KEY}", "")
            username = BuiltIn().get_variable_value("${RANCHER_USERNAME}", "admin")
            password = BuiltIn().get_variable_value("${RANCHER_PASSWORD}", "password1234")
        except Exception:
            api_key = os.getenv("RANCHER_API_KEY", "")
            username = os.getenv("RANCHER_USERNAME", "admin")
            password = os.getenv("RANCHER_PASSWORD", "password1234")

        session = requests.Session()
        session.verify = False
        session.headers.update({"Content-Type": "application/json"})

        if api_key:
            token = api_key.removeprefix("Bearer ").strip()
            session.headers.update({"Authorization": f"Bearer {token}"})
            self.rancher_token = token
            self.rancher_session = session
            logging(f"Authenticating with Rancher at {rancher_endpoint} using API key")
            return

        logging(f"Authenticating with Rancher at {rancher_endpoint} as user {username}")

        from urllib.parse import urljoin
        url = urljoin(rancher_endpoint, "v3-public/localProviders/local?action=login")
        logging(f"Auth URL: {url}")

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

        session.headers.update({"Authorization": f"Bearer {token}"})

        self.rancher_token = token
        self.rancher_session = session
        logging("Successfully authenticated with Rancher")

    def _rancher_request(self, method, path, data=None, content_type=None):
        """Make request to Rancher API"""
        # Ensure authenticated
        self._authenticate_rancher()

        url = f"{self.rancher_endpoint.rstrip('/')}/{path.lstrip('/')}"
        logging(f"Rancher API request: {method} {url}")

        try:
            if method.upper() == "GET":
                response = self.rancher_session.get(url)
            elif method.upper() == "POST":
                response = self.rancher_session.post(url, json=data)
            elif method.upper() == "PUT":
                response = self.rancher_session.put(url, json=data)
            elif method.upper() == "PATCH":
                headers = {}
                if content_type:
                    headers["Content-Type"] = content_type
                response = self.rancher_session.patch(
                    url, json=data, headers=headers
                )
            elif method.upper() == "DELETE":
                response = self.rancher_session.delete(url)
            else:
                raise ValueError(f"Unsupported HTTP method: {method}")

            try:
                response_data = response.json()
            except json.JSONDecodeError:
                response_data = response.text

            return response.status_code, response_data
        except Exception as e:
            raise Exception(f"Rancher API request failed: {e}")

    def _rancher_proxy_request(self, method, cluster_id, path, data=None):
        """Make a request to the Rancher proxy API for a guest cluster"""
        return self._rancher_request(
            method, f"k8s/clusters/{cluster_id}/{path.lstrip('/')}", data
        )

    def create_harvester_mgmt_cluster(self, cluster_name):
        """Create Harvester management cluster entry in Rancher (Import Existing)"""
        logging(f"Creating Harvester management cluster entry: {cluster_name}")

        payload = {
            "type": "provisioning.cattle.io.cluster",
            "metadata": {
                "name": cluster_name,
                "namespace": "fleet-default",
                "labels": {
                    "provider.cattle.io": "harvester"
                }
            },
            "spec": {}
        }

        code, data = self._rancher_request(
            "POST",
            "v1/provisioning.cattle.io.clusters",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create Harvester mgmt cluster: {code}, {data}")

        logging(f"Created Harvester management cluster: {cluster_name}")
        return data

    def get_harvester_mgmt_cluster(self, cluster_name):
        """Get Harvester management cluster details"""
        logging(f"Getting Harvester management cluster: {cluster_name}")

        code, data = self._rancher_request(
            "GET",
            f"v1/provisioning.cattle.io.clusters/fleet-default/{cluster_name}"
        )

        if code == 404:
            logging(f"Harvester mgmt cluster {cluster_name} not found")
            return None
        elif code != 200:
            raise Exception(f"Failed to get Harvester mgmt cluster: {code}, {data}")

        return data

    def delete_harvester_mgmt_cluster(self, cluster_name):
        """Delete Harvester management cluster entry"""
        logging(f"Deleting Harvester management cluster: {cluster_name}")

        code, data = self._rancher_request(
            "DELETE",
            f"v1/provisioning.cattle.io.clusters/fleet-default/{cluster_name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(f"Failed to delete Harvester mgmt cluster: {code}, {data}")

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

    def get_cluster_registration_url(self, cluster_id, rancher_endpoint=None, timeout=300):
        """Get cluster registration URL for importing Harvester"""
        logging(f"Getting cluster registration URL for cluster: {cluster_id}")

        # Authenticate first
        self._authenticate_rancher(rancher_endpoint)

        logging(f"Polling Rancher API for registration token: {cluster_id}:default-token")

        # Poll for the registration token to appear
        retry_count, retry_interval = get_retry_count_and_interval()
        end_time = time.time() + int(timeout)
        attempt = 0

        while time.time() < end_time:
            attempt += 1
            code, data = self._rancher_request(
                "GET",
                f"v3/clusterRegistrationTokens/{cluster_id}:default-token"
            )

            # Log every 5 attempts to show progress
            if attempt % 5 == 1 or code not in [200, 404]:
                logging(f"Attempt {attempt}: Response status {code}")

            if code == 200:
                manifest_url = data.get("manifestUrl")
                if manifest_url:
                    logging(f"Got cluster registration URL: {manifest_url}")
                    return manifest_url
                else:
                    if attempt % 5 == 1:
                        logging("manifestUrl not yet available in response, waiting...")
            elif code == 404:
                if attempt % 5 == 1:
                    logging("Registration token not yet created, waiting...")
            else:
                logging(f"Unexpected response {code}: {data}", level="WARNING")

            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for cluster registration URL for {cluster_id} "
            f"after {attempt} attempts"
        )

    def set_cluster_registration_url(self, url):
        """Set cluster-registration-url setting in Harvester

        The value must be in JSON format matching Harvester structure:
        {'url': '...', 'insecureSkipTLSVerify': true/false}
        """
        logging(f"Setting cluster-registration-url to: {url}")

        # Format the value as JSON object (matching Harvester's default structure)
        if url:
            value_obj = {"url": url, "insecureSkipTLSVerify": True}
            value = json.dumps(value_obj)
        else:
            value = '{"url":"","insecureSkipTLSVerify":false}'

        # Get current setting first
        code, data = self.harvester_api.settings.get("cluster-registration-url")
        if code != 200:
            raise Exception(f"Failed to get cluster-registration-url setting: {code}, {data}")

        # Update the value field with the JSON string
        data["value"] = value

        # Use the API's internal _put method directly
        path = f"apis/{self.harvester_api.API_VERSION}/settings/cluster-registration-url"
        resp = self.harvester_api._put(path, json=data)

        if resp.status_code not in [200, 201]:
            try:
                error_data = resp.json()
            except Exception:
                error_data = resp.text
            raise Exception(
                f"Failed to set cluster-registration-url: "
                f"{resp.status_code}, {error_data}"
            )

        logging("Successfully set cluster-registration-url")

    def get_all_rke2_versions(self, rancher_endpoint=None, max_versions=None):
        """
        Get all available RKE2 versions from Rancher.

        Args:
            rancher_endpoint: Rancher server endpoint (uses self.rancher_api if not provided)
            max_versions: Maximum number of versions to return (None = all)

        Returns:
            List of version strings sorted by semantic version (newest first)
        """
        logging("Getting all RKE2 versions from Rancher")

        code, data = self._rancher_request("GET", "v1-rke2-release/releases")

        if code != 200:
            raise Exception(f"Failed to get RKE2 versions: {code}, {data}")

        versions = [r['id'] for r in data.get('data', [])]

        if not versions:
            raise Exception("No RKE2 versions available from Rancher")

        # Sort versions by semantic version (descending)
        from pkg_resources import parse_version
        sorted_versions = sorted(
            versions,
            key=lambda v: parse_version(v.split("+")[0].split("-")[0]),
            reverse=True
        )

        # Apply max_versions limit if specified
        if max_versions and max_versions > 0:
            sorted_versions = sorted_versions[:max_versions]

        logging(f"Found {len(sorted_versions)} RKE2 versions")
        return sorted_versions

    def get_rke2_version(self, target_version, rancher_endpoint=None):
        """
        Get RKE2 version from Rancher that matches target version.

        Args:
            target_version: Target version prefix (e.g. 'v1.28', 'v1.29')
            rancher_endpoint: Rancher server endpoint (uses self.rancher_api if not provided)

        Returns:
            Full version string (e.g. 'v1.28.15+rke2r1')
        """
        logging(f"Getting RKE2 version for target: {target_version}")

        code, data = self._rancher_request("GET", "v1-rke2-release/releases")

        if code != 200:
            raise Exception(f"Failed to get RKE2 versions: {code}, {data}")

        versions = [r['id'] for r in data.get('data', [])]

        if not versions:
            raise Exception("No RKE2 versions available from Rancher")

        # Sort versions by semantic version (descending)
        from pkg_resources import parse_version
        sorted_versions = sorted(
            versions,
            key=lambda v: parse_version(v.split("+")[0].split("-")[0]),
            reverse=True
        )

        # Find first version matching target prefix
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
        code, data = self._rancher_request("GET", "v3/nodeDrivers/harvester")
        if code != 200:
            raise Exception(f"Failed to get harvester node driver: {code}, {data}")

        url = data.get("url", "")
        match = re.search(r"/v(\d+\.\d+\.\d+)/", url)
        if not match:
            raise Exception(f"Could not parse version from node driver url: {url}")
        return match.group(1)

    def configure_kdm_url(self, url):
        """
        Update the Rancher global setting rke-metadata-config to use a custom KDM URL.

        Fetches the current setting value, updates the 'url' field and sets
        'refresh-interval-minutes' to '0', then writes it back so Rancher fetches the
        new data.json immediately.

        Args:
            url: URL of the custom KDM data.json file
        """
        logging(f"Configuring rke-metadata-config KDM URL: {url}")

        code, data = self._rancher_request(
            "GET", "v1/management.cattle.io.settings/rke-metadata-config"
        )
        if code != 200:
            raise Exception(f"Failed to get rke-metadata-config setting: {code}, {data}")

        current_value = data.get("value") or data.get("default", "{}")
        try:
            config = json.loads(current_value)
        except (json.JSONDecodeError, TypeError):
            config = {}

        config["url"] = url
        config["refresh-interval-minutes"] = "0"

        updated_data = dict(data)
        updated_data["value"] = json.dumps(config)

        code, result = self._rancher_request(
            "PUT", "v1/management.cattle.io.settings/rke-metadata-config", updated_data
        )
        if code not in [200, 201]:
            raise Exception(f"Failed to update rke-metadata-config: {code}, {result}")

        logging(f"rke-metadata-config updated: url={url}, refresh-interval-minutes=0")

    def create_cloud_credential(self, name, kubeconfig, cluster_id):
        """Create cloud credential for Harvester"""
        logging(f"Creating cloud credential: {name}")

        payload = {
            "type": "cloudCredential",
            "name": name,
            "harvestercredentialConfig": {
                "clusterId": cluster_id,
                "clusterType": "imported",
                "kubeconfigContent": kubeconfig
            }
        }

        code, data = self._rancher_request("POST", "v3/cloudcredentials", payload)

        if code not in [200, 201]:
            raise Exception(f"Failed to create cloud credential: {code}, {data}")

        logging(f"Created cloud credential: {name}")
        return data

    def get_cloud_credential(self, credential_id):
        """Get cloud credential details"""
        logging(f"Getting cloud credential: {credential_id}")

        code, data = self._rancher_request("GET", f"v3/cloudCredentials/{credential_id}")

        if code == 404:
            return None
        elif code != 200:
            raise Exception(f"Failed to get cloud credential: {code}, {data}")

        return data

    def delete_cloud_credential(self, credential_id):
        """Delete cloud credential"""
        logging(f"Deleting cloud credential: {credential_id}")

        code, data = self._rancher_request("DELETE", f"v3/cloudCredentials/{credential_id}")

        if code not in [200, 204, 404]:
            raise Exception(f"Failed to delete cloud credential: {code}, {data}")

        logging(f"Deleted cloud credential: {credential_id}")

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

        payload = {
            "type": "provisioning.cattle.io.cluster",
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

        code, data = self._rancher_request(
            "POST",
            "v1/provisioning.cattle.io.clusters",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create RKE2 cluster: {code}, {data}")

        logging(f"Created RKE2 cluster: {name}")
        return data

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

                    # Count actual running machines via Rancher API
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
        """Count machines in Running phase for a given cluster via Rancher API."""
        try:
            code, data = self._rancher_request(
                "GET",
                f"v1/cluster.x-k8s.io.machines/fleet-default"
                f"?labelSelector=cluster.x-k8s.io/cluster-name={cluster_name}"
            )
            if code != 200:
                return 0
            items = data.get("data", []) if isinstance(data, dict) else []
            return sum(1 for m in items
                       if m.get("status", {}).get("phase") == "Running")
        except Exception:
            return 0

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
        """Scale RKE2 cluster by adding/removing a worker-only machine pool."""
        logging(f"Scaling RKE2 cluster {cluster_name}: worker_count={worker_count}")

        cluster = self.get_rke2_cluster(cluster_name)
        if not cluster:
            raise Exception(f"RKE2 cluster {cluster_name} not found")

        machine_pools = cluster.get("spec", {}).get("rkeConfig", {}).get("machinePools", [])

        # Remove existing worker-pool if present
        machine_pools = [p for p in machine_pools if p.get("name") != "worker-pool"]

        if int(worker_count) > 0:
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

        # Update the cluster via PUT
        cluster["spec"]["rkeConfig"]["machinePools"] = machine_pools

        code, data = self._rancher_request(
            "PUT",
            f"v1/provisioning.cattle.io.clusters/fleet-default/{cluster_name}",
            cluster
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to scale RKE2 cluster: {code}, {data}")

        logging(f"Scaled RKE2 cluster {cluster_name} worker pool to {worker_count}")

    def upgrade_rke2_cluster(self, cluster_name, new_k8s_version):
        """Upgrade RKE2 cluster to a new Kubernetes version."""
        logging(f"Upgrading RKE2 cluster {cluster_name} to {new_k8s_version}")

        cluster = self.get_rke2_cluster(cluster_name)
        if not cluster:
            raise Exception(f"RKE2 cluster {cluster_name} not found")

        cluster["spec"]["kubernetesVersion"] = new_k8s_version

        code, data = self._rancher_request(
            "PUT",
            f"v1/provisioning.cattle.io.clusters/fleet-default/{cluster_name}",
            cluster
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to upgrade RKE2 cluster: {code}, {data}")

        logging(f"Triggered upgrade of RKE2 cluster {cluster_name} to {new_k8s_version}")

    def create_harvester_config(self, name, cpus, mems, disks, image_id,
                                network_id, ssh_user, user_data):
        """Create Harvester config for RKE2 node template"""
        logging(f"Creating Harvester config: {name}")

        import base64

        # Build diskInfo and networkInfo as JSON structures
        import json
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

        payload = {
            "type": "rke-machine-config.cattle.io.harvesterconfig",
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

        code, data = self._rancher_request(
            "POST",
            "v1/rke-machine-config.cattle.io.harvesterconfigs",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create Harvester config: {code}, {data}")

        logging(f"Created Harvester config: {name}")
        return data

    def generate_kubeconfig(self, cluster_id, cluster_name):
        """Generate full-access kubeconfig for Harvester cluster.

        Uses Rancher's generateKubeconfig action to get a kubeconfig with
        full access to the Harvester cluster. Used for cloud credentials.
        """
        logging(f"Generating kubeconfig for cluster: {cluster_name}")

        code, data = self._rancher_request(
            "POST",
            f"v3/clusters/{cluster_id}?action=generateKubeconfig"
        )

        if code != 200:
            raise Exception(f"Failed to generate kubeconfig: {code}, {data}")

        logging("Generated kubeconfig for cluster")
        return data.get("config", "")

    def generate_cloud_provider_kubeconfig(self, cluster_id, cluster_name):
        """Generate cloud provider kubeconfig via Harvester kubeconfig API.

        Uses the Harvester-specific endpoint to generate a kubeconfig with
        the external Rancher URL and limited cloudprovider role. This is
        used for the cloud provider secret inside the guest VM.
        """
        logging(f"Generating cloud provider kubeconfig for cluster: {cluster_name}")

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/harvester/kubeconfig",
            {
                "clusterRoleName": "harvesterhci.io:cloudprovider",
                "namespace": "default",
                "serviceAccountName": cluster_name
            }
        )

        if code != 200:
            raise Exception(f"Failed to generate harvester kubeconfig: {code}, {data}")

        # Response is a JSON-encoded string with \n escapes
        if isinstance(data, str):
            kubeconfig = data.replace("\\n", "\n").strip('"')
        else:
            kubeconfig = str(data)

        logging("Generated cloud provider kubeconfig for cluster")
        return kubeconfig

    def create_secret(self, name, data, annotations):
        """Create secret for cloud provider config"""
        logging(f"Creating secret: {name}")

        payload = {
            "type": "secret",
            "metadata": {
                "name": name,
                "namespace": "fleet-default",
                "annotations": annotations
            },
            "stringData": data
        }

        code, resp_data = self._rancher_request(
            "POST",
            "v1/secrets/fleet-default",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create secret: {code}, {resp_data}")

        logging(f"Created secret: {name}")
        return resp_data

    def create_deployment(self, cluster_id, namespace, name, image, pvc=None):
        """Create deployment in guest cluster"""
        logging(f"Creating deployment {name} in cluster {cluster_id}")

        container = {
            "name": name,
            "image": image,
            "ports": [{"containerPort": 80}]
        }

        payload = {
            "type": "apps.deployment",
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
            payload["spec"]["template"]["spec"]["volumes"] = [
                {"name": "data", "persistentVolumeClaim": {"claimName": pvc}}
            ]
            payload["spec"]["template"]["spec"]["containers"][0]["volumeMounts"] = [
                {"name": "data", "mountPath": "/data"}
            ]

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/apps.deployments/{namespace}",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create deployment: {code}, {data}")

        logging(f"Created deployment {name}")
        return data

    def get_deployment(self, cluster_id, namespace, name):
        """Get deployment details from guest cluster"""

        code, data = self._rancher_request(
            "GET",
            f"k8s/clusters/{cluster_id}/v1/apps.deployments/{namespace}/{name}"
        )

        if code == 404:
            return None
        elif code != 200:
            raise Exception(f"Failed to get deployment: {code}, {data}")

        return data

    def delete_deployment(self, cluster_id, namespace, name):
        """Delete deployment from guest cluster"""
        logging(f"Deleting deployment {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "DELETE",
            f"k8s/clusters/{cluster_id}/v1/apps.deployments/{namespace}/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(f"Failed to delete deployment: {code}, {data}")

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
                    state = deployment.get("metadata", {}).get("state", {}).get("name", "")
                    if state == "active":
                        logging(f"Deployment {name} is ready")
                        return deployment
                    now = time.time()
                    if now - last_log_time >= 30:
                        logging(f"Deployment {name} state: {state}")
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

        deployment = self.get_deployment(cluster_id, namespace, name)
        if deployment is None:
            raise Exception(f"Deployment {name} not found")

        deployment.setdefault("spec", {})["replicas"] = int(replicas)

        code, data = self._rancher_request(
            "PUT",
            f"k8s/clusters/{cluster_id}/v1/apps.deployments/{namespace}/{name}",
            deployment
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to scale deployment {name}: {code}, {data}")

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
        """Create PVC in guest cluster"""
        logging(f"Creating PVC {name} in cluster {cluster_id}")

        payload = {
            "type": "persistentvolumeclaim",
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
            payload["spec"]["storageClassName"] = storage_class

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/persistentvolumeclaims/{DEFAULT_NAMESPACE}",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create PVC: {code}, {data}")

        logging(f"Created PVC {name}")
        return data

    def get_pvc(self, cluster_id, name):
        """Get PVC details from guest cluster"""
        logging(f"Getting PVC {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "GET",
            f"k8s/clusters/{cluster_id}/v1/persistentvolumeclaims/{DEFAULT_NAMESPACE}/{name}"
        )

        if code == 404:
            return None
        elif code != 200:
            raise Exception(f"Failed to get PVC: {code}, {data}")

        return data

    def delete_pvc(self, cluster_id, name):
        """Delete PVC from guest cluster"""
        logging(f"Deleting PVC {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "DELETE",
            f"k8s/clusters/{cluster_id}/v1/persistentvolumeclaims/{DEFAULT_NAMESPACE}/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(f"Failed to delete PVC: {code}, {data}")

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
                    state = pvc.get("metadata", {}).get("state", {}).get("name", "")
                    if state == "bound":
                        logging(f"PVC {name} is bound")
                        return pvc
            except Exception as e:
                logging(f"Error checking PVC status: {e}", level="WARNING")

            time.sleep(retry_interval)

        raise Exception(f"Timeout waiting for PVC {name} to be bound")

    def create_lb_service(self, cluster_id, service_data):
        """Create LoadBalancer service"""
        name = service_data["metadata"]["name"]
        namespace = service_data["metadata"]["namespace"]
        logging(f"Creating LoadBalancer service {name} in cluster {cluster_id}")

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/services/{namespace}",
            service_data
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create LoadBalancer service: {code}, {data}")

        logging(f"Created LoadBalancer service {name}")
        return data

    def get_lb_service(self, cluster_id, name):
        """Get LoadBalancer service details"""
        logging(f"Getting LoadBalancer service {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "GET",
            f"k8s/clusters/{cluster_id}/v1/services/{DEFAULT_NAMESPACE}/{name}"
        )

        if code == 404:
            return None
        elif code != 200:
            raise Exception(f"Failed to get LoadBalancer service: {code}, {data}")

        return data

    def delete_lb_service(self, cluster_id, name):
        """Delete LoadBalancer service"""
        logging(f"Deleting LoadBalancer service {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "DELETE",
            f"k8s/clusters/{cluster_id}/v1/services/{DEFAULT_NAMESPACE}/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(f"Failed to delete LoadBalancer service: {code}, {data}")

        logging(f"Deleted LoadBalancer service {name}")

    def wait_for_lb_service_ready(self, cluster_id, name, timeout=DEFAULT_TIMEOUT):
        """Wait for LoadBalancer service to be ready"""
        logging(f"Waiting for LoadBalancer service {name} to be ready")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            try:
                service = self.get_lb_service(cluster_id, name)
                if service:
                    state = service.get("metadata", {}).get("state", {}).get("name", "")
                    if state == "active":
                        ingress = (
                            service.get("status", {})
                            .get("loadBalancer", {})
                            .get("ingress", [])
                        )
                        if ingress and ingress[0].get("ip"):
                            logging(
                                f"LoadBalancer service {name} is ready "
                                f"with IP {ingress[0]['ip']}"
                            )
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

        Uses /k8s/clusters/{id}/api/v1/namespaces/{ns}/services/{svc}:{port}/proxy/
        which works regardless of whether the LB VIP is routable from the test runner.
        """
        proxy_path = (f"k8s/clusters/{cluster_id}/api/v1/namespaces/{namespace}/"
                      f"services/{service_name}:{port}/proxy/")
        logging(f"Querying LB via Rancher proxy: {proxy_path}")
        last_err = None
        for attempt in range(retries):
            try:
                code, data = self._rancher_request("GET", proxy_path)
                return code, data if isinstance(data, str) else str(data)
            except Exception as e:
                last_err = e
                if attempt < retries - 1:
                    logging(f"LB proxy query attempt {attempt + 1}/{retries} "
                            f"failed: {e}, retrying...")
                    time.sleep(interval)
        raise Exception(f"Failed to query LB via proxy after {retries} attempts: {last_err}")

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

        self._authenticate_rancher()

        payload = {
            "type": "provisioning.cattle.io.cluster",
            "metadata": {
                "name": name,
                "namespace": "fleet-default"
            },
            "spec": {}
        }

        code, data = self._rancher_request(
            "POST",
            "v1/provisioning.cattle.io.clusters",
            payload
        )

        if code not in [200, 201]:
            raise Exception(
                f"Failed to create import cluster: {code}, {data}"
            )

        logging(f"Created import cluster: {name}")
        return data

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
                        mgmt_code, mgmt_data = self._rancher_request(
                            "GET",
                            f"v1/management.cattle.io.clusters/{cluster_id}"
                        )
                        if mgmt_code == 200 and mgmt_data:
                            mgmt_conditions = {
                                c.get("type"): c.get("status")
                                for c in mgmt_data.get("status", {})
                                                  .get("conditions", [])
                            }
                            connected = mgmt_conditions.get("Connected") == "True"
                            ready_cond = mgmt_conditions.get("Ready") == "True"

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
        """Create a custom RKE2 cluster without machinePools.

        Nodes are registered externally via the registration command.
        The Harvester cloud provider is still configured so that
        harvester-cloud-provider and harvester-csi-driver function correctly.
        """
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

        payload = {
            "type": "provisioning.cattle.io.cluster",
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

        code, data = self._rancher_request(
            "POST",
            "v1/provisioning.cattle.io.clusters",
            payload
        )

        if code not in [200, 201]:
            raise Exception(
                f"Failed to create custom RKE2 cluster: {code}, {data}"
            )

        logging(f"Created custom RKE2 cluster: {name}")
        return data

    def update_cluster_chart_name(self, cluster_name, mgmt_cluster_id):
        """Patch the custom cluster's chartValues with the real management ID."""
        logging(f"Updating clusterName to {mgmt_cluster_id} for {cluster_name}")

        self._authenticate_rancher()

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

        code, data = self._rancher_request(
            "PATCH",
            f"v1/provisioning.cattle.io.clusters/fleet-default/{cluster_name}",
            patch,
            content_type="application/merge-patch+json"
        )

        if code not in [200, 201]:
            raise Exception(
                f"Failed to update clusterName for {cluster_name}: "
                f"{code}, {data}"
            )

        logging(f"Updated clusterName to {mgmt_cluster_id}")

    def fix_cloud_provider_cluster_name(self, cluster_id):
        """Fix the cloud-provider --cluster-name arg on the guest cluster.

        See crd.py docstring for full explanation.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(f"Fixing cloud provider clusterName to {cluster_id}")

        # Wait for the cloud-provider deployment to exist
        deploy = self.wait_for_deployment_ready(
            cluster_id, "kube-system", "harvester-cloud-provider"
        )

        containers = deploy.get("spec", {}).get("template", {}).get(
            "spec", {}).get("containers", [])

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
                json_patch = [{
                    "op": "add",
                    "path": (
                        f"/spec/template/spec/containers/{idx}/args/-"
                    ),
                    "value": f"--cluster-name={cluster_id}"
                }]

            code, data = self._rancher_request(
                "PATCH",
                f"k8s/clusters/{cluster_id}/apis/apps/v1"
                f"/namespaces/kube-system"
                f"/deployments/harvester-cloud-provider",
                data=json_patch,
                content_type="application/json-patch+json"
            )

            if code not in [200, 201]:
                raise Exception(
                    f"Failed to patch cloud-provider: {code}"
                )

            logging(f"Patched --cluster-name to {cluster_id}")

            # Wait for the rolling update to complete
            self.wait_for_deployment_ready(
                cluster_id, "kube-system", "harvester-cloud-provider"
            )
            logging("Cloud provider deployment rolled out with "
                    "correct clusterName")
            return

        raise Exception(
            "Could not find cloud-provider container in deployment"
        )

    def get_cluster_registration_command(self, cluster_name, timeout=300):
        """Get the insecure node registration command for a custom cluster."""
        logging(f"Getting registration command for cluster: {cluster_name}")

        self._authenticate_rancher()

        # First, get the management cluster ID
        cluster = self.get_rke2_cluster(cluster_name)
        if not cluster:
            raise Exception(f"Cluster {cluster_name} not found")

        cluster_id = cluster.get("status", {}).get("clusterName", "")
        if not cluster_id:
            raise Exception(
                f"Cluster {cluster_name} has no management cluster ID yet"
            )

        logging(f"Management cluster ID: {cluster_id}")

        retry_count, retry_interval = get_retry_count_and_interval()
        end_time = time.time() + int(timeout)
        attempt = 0

        while time.time() < end_time:
            attempt += 1
            code, data = self._rancher_request(
                "GET",
                f"v3/clusterRegistrationTokens/{cluster_id}:default-token"
            )

            if attempt % 5 == 1 or code not in [200, 404]:
                logging(f"Attempt {attempt}: Response status {code}")

            if code == 200:
                node_cmd = data.get("insecureNodeCommand", "")
                if node_cmd:
                    logging("Got cluster registration command")
                    return node_cmd
                elif attempt % 5 == 1:
                    logging("insecureNodeCommand not yet available, waiting...")
            elif code == 404:
                if attempt % 5 == 1:
                    logging("Registration token not yet created, waiting...")
            else:
                logging(
                    f"Unexpected response {code}: {data}", level="WARNING"
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
        """Create a VM on Harvester using the Harvester REST API.

        Builds the VirtualMachine manifest directly (same pattern as vm/crd.py)
        to avoid relying on VMManager.Spec from libs/harvester_api.py, which
        does not properly build the Harvester manifest structure.
        """
        logging(f"Creating Harvester VM: {name}")

        disk_name = f"{name}-disk-0"
        disk_size_gi = f"{disk_size}Gi"
        memory_gi = f"{memory}Gi" if str(memory).isdigit() else str(memory)

        # Look up the image's actual storage class from Harvester.
        # Harvester dynamically creates a storage class (lh-<uuid>) per image;
        # the name cannot be guessed — it must be read from the image status.
        image_ns, image_name = (
            image_id.split("/", 1) if "/" in image_id
            else (DEFAULT_NAMESPACE, image_id)
        )
        img_path = f"/v1/harvesterhci.io.virtualmachineimages/{image_ns}/{image_name}"
        img_code, img_data = self.harvester_api.get(img_path)
        if img_code != 200:
            raise Exception(
                f"Failed to look up image {image_id}: {img_code}, {img_data}"
            )
        storage_class = img_data.get("status", {}).get("storageClassName", "")
        if not storage_class:
            raise Exception(
                f"Image {image_id} has no storageClassName in status"
            )
        logging(f"Resolved storage class for image {image_id}: {storage_class}")

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

        volumes = [
            {"name": "rootdisk",
             "persistentVolumeClaim": {"claimName": disk_name}},
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

        path = f"/v1/kubevirt.io.virtualmachines/{DEFAULT_NAMESPACE}"
        code, data = self.harvester_api.post(path, data=vm_dict)
        if code not in [200, 201]:
            raise Exception(f"Failed to create VM {name}: {code}, {data}")

        logging(f"Created Harvester VM: {name}")
        return data

    # Chart Install Operations (for import clusters)
    def install_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Install a Helm chart on a guest cluster via Rancher catalog API."""
        logging(f"Installing chart {chart_name} v{version} as {release_name} "
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

        path = (
            f"k8s/clusters/{cluster_id}"
            f"/v1/catalog.cattle.io.clusterrepos/{repo_name}"
            f"?action=install"
        )
        max_attempts = 5
        retry_wait_seconds = 30

        for attempt in range(1, max_attempts + 1):
            code, data = self._rancher_request("POST", path, data=payload)
            if code in (200, 201):
                break
            if "lost connection to cluster" not in str(data) or attempt == max_attempts:
                raise Exception(
                    f"Failed to install chart {chart_name}: "
                    f"{code}, {str(data)[:500]}"
                )
            logging(
                f"Chart install hit a transient tunnel disconnect "
                f"(attempt {attempt}/{max_attempts}); retrying in "
                f"{retry_wait_seconds}s",
                level="WARNING"
            )
            time.sleep(retry_wait_seconds)

        logging(f"Chart install initiated: {chart_name} v{version}")
        return data

    def upgrade_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Upgrade an installed Helm chart on a guest cluster via Rancher
        catalog API using the 'upgrade' action."""
        logging(f"Upgrading chart {chart_name} to v{version} as "
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

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}"
            f"/v1/catalog.cattle.io.clusterrepos/{repo_name}"
            f"?action=upgrade",
            data=payload
        )

        if code not in [200, 201]:
            raise Exception(
                f"Failed to upgrade chart {chart_name}: "
                f"{code}, {str(data)[:500]}"
            )

        logging(f"Chart upgrade initiated: {chart_name} v{version}")
        return data

    def uninstall_chart(self, cluster_id, release_name, namespace):
        """Uninstall a Helm chart (app) from a guest cluster via Rancher
        catalog API."""
        logging(f"Uninstalling chart {release_name} from {namespace} "
                f"on cluster {cluster_id}")

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}"
            f"/v1/catalog.cattle.io.apps/{namespace}/{release_name}"
            f"?action=uninstall",
            data={}
        )

        if code == 404:
            logging(f"Chart app {release_name} not found; "
                    f"nothing to uninstall")
            return {}
        if code not in [200, 201]:
            raise Exception(
                f"Failed to uninstall chart {release_name}: "
                f"{code}, {str(data)[:500]}"
            )

        logging(f"Chart uninstall initiated: {release_name}")
        return data

    def wait_for_chart_app_deleted(self, cluster_id, release_name, namespace,
                                   timeout=DEFAULT_TIMEOUT):
        """Wait for a chart app to be fully removed from a guest cluster."""
        logging(f"Waiting for chart app {release_name} to be deleted "
                f"from {namespace}")
        retry_count, retry_interval = get_retry_count_and_interval()

        end_time = time.time() + int(timeout)
        while time.time() < end_time:
            code, data = self._rancher_proxy_request(
                "GET", cluster_id,
                f"v1/catalog.cattle.io.apps/{namespace}/{release_name}"
            )
            if code == 404:
                logging(f"Chart app {release_name} has been deleted")
                return True
            time.sleep(retry_interval)

        raise Exception(
            f"Timeout waiting for chart app {release_name} to be deleted"
        )

    def create_cluster_repo(self, cluster_id, repo_name, git_url, git_branch):
        """Create a ClusterRepo on a guest cluster via Rancher proxy."""
        logging(f"Creating ClusterRepo {repo_name} on cluster {cluster_id} "
                f"(branch={git_branch})")

        payload = {
            "type": "catalog.cattle.io.clusterrepo",
            "metadata": {
                "name": repo_name
            },
            "spec": {
                "gitRepo": git_url,
                "gitBranch": git_branch
            }
        }

        code, data = self._rancher_proxy_request(
            "POST", cluster_id,
            "v1/catalog.cattle.io.clusterrepos",
            payload
        )

        if code == 409:
            logging(f"ClusterRepo {repo_name} already exists")
            return data
        if code not in [200, 201]:
            raise Exception(
                f"Failed to create ClusterRepo {repo_name}: "
                f"{code}, {str(data)[:500]}"
            )

        logging(f"Created ClusterRepo {repo_name}")
        return data

    def wait_for_cluster_repo_ready(self, cluster_id, repo_name,
                                    timeout=DEFAULT_TIMEOUT,
                                    expected_git_branch=None):
        """Wait for a ClusterRepo to finish downloading on a guest cluster.

        Requires Downloaded=True on two consecutive polls to avoid the
        false-positive that occurs briefly after initial creation.

        """
        logging(f"Waiting for ClusterRepo {repo_name} to be ready"
                + (f" (branch={expected_git_branch})"
                   if expected_git_branch else ""))
        end_time = time.time() + int(timeout)
        consecutive_ready = 0
        reapply_count = 0
        max_reapply = 3

        while time.time() < end_time:
            code, data = self._rancher_proxy_request(
                "GET", cluster_id,
                f"v1/catalog.cattle.io.clusterrepos/{repo_name}"
            )
            downloaded = False
            if code == 200 and data:
                conditions = data.get("status", {}).get("conditions", [])
                for c in conditions:
                    if (c.get("type") == "Downloaded" and
                            c.get("status") == "True"):
                        downloaded = True
                        break

            if downloaded:
                consecutive_ready += 1
                if consecutive_ready >= 2:
                    if expected_git_branch:
                        spec = data.get("spec", {})
                        current_branch = spec.get("gitBranch", "")

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
                                f"re-applying via PUT "
                                f"(attempt {reapply_count + 1}/{max_reapply})",
                                level="WARNING"
                            )
                            updated = dict(data)
                            updated.setdefault("spec", {})
                            updated["spec"]["gitBranch"] = expected_git_branch
                            self._rancher_proxy_request(
                                "PUT", cluster_id,
                                f"v1/catalog.cattle.io.clusterrepos/{repo_name}",
                                updated
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
        """
        logging(f"Getting versions for chart {chart_name} from {repo_name}")

        max_attempts = 12 if cluster_id else 1
        for attempt in range(max_attempts):
            if cluster_id:
                code, data = self._rancher_proxy_request(
                    "GET", cluster_id,
                    f"v1/catalog.cattle.io.clusterrepos/{repo_name}"
                    f"?link=index"
                )
            else:
                code, data = self._rancher_request(
                    "GET",
                    f"v1/catalog.cattle.io.clusterrepos/{repo_name}"
                    f"?link=index"
                )

            if code != 200:
                if attempt < max_attempts - 1:
                    logging(f"Chart index not available yet, retrying "
                            f"({attempt + 1}/{max_attempts})...")
                    time.sleep(5)
                    continue
                raise Exception(
                    f"Failed to get chart index: {code}"
                )

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

        Reads the version via the Rancher proxy API.

        Args:
            cluster_id: Rancher management cluster ID
            release_name: Helm release name (e.g. harvester-csi-driver)
            namespace: Namespace where the chart was installed

        Returns:
            str: Deployed chart version (e.g. '0.1.18')
        """
        code, data = self._rancher_proxy_request(
            "GET", cluster_id,
            f"v1/catalog.cattle.io.apps/{namespace}/{release_name}"
        )
        if code != 200:
            raise Exception(
                f"Failed to get chart app {release_name}: {code}, {data}")
        version = (data.get("spec", {})
                       .get("chart", {})
                       .get("metadata", {})
                       .get("version", "unknown"))
        logging(f"Deployed chart version for {release_name}: {version}")
        return version

    def create_cloud_config_secret(self, cluster_id, secret_name,
                                   namespace, kubeconfig):
        """Create a cloud-provider-config secret on a guest cluster."""
        logging(f"Creating cloud config secret {secret_name} in "
                f"{namespace} on cluster {cluster_id}")

        import base64
        encoded = base64.b64encode(
            kubeconfig.encode("utf-8")
        ).decode("utf-8")

        secret_data = {
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

        code, data = self._rancher_proxy_request(
            "POST", cluster_id,
            f"api/v1/namespaces/{namespace}/secrets",
            secret_data
        )

        if code not in [200, 201]:
            if code == 409:
                logging(f"Secret {secret_name} already exists")
                return secret_data
            raise Exception(
                f"Failed to create cloud config secret: {code}, "
                f"{str(data)[:300]}"
            )

        logging(f"Created cloud config secret: {secret_name}")
        return data

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
        code, data = self._rancher_proxy_request(
            "POST", cluster_id,
            f"apis/apps/v1/namespaces/{namespace}/daemonsets",
            ds_spec
        )
        if code not in [200, 201]:
            if code == 409:
                logging(f"DaemonSet {ds_name} already exists, proceeding")
            else:
                raise Exception(
                    f"Failed to deploy cloud-config writer DaemonSet: "
                    f"{code}, {str(data)[:300]}"
                )

        # Wait until all scheduled nodes have the initContainer completed
        import time as _time
        end_time = _time.time() + 300
        while _time.time() < end_time:
            code, ds_data = self._rancher_proxy_request(
                "GET", cluster_id,
                f"apis/apps/v1/namespaces/{namespace}/daemonsets/{ds_name}"
            )
            if code == 200 and ds_data:
                status = ds_data.get("status", {})
                desired = status.get("desiredNumberScheduled", 0)
                ready = status.get("numberReady", 0)
                if desired > 0 and desired == ready:
                    logging(f"All {ready}/{desired} nodes configured")
                    break
            _time.sleep(10)
        else:
            raise Exception(
                "Timed out waiting for cloud-config writer DaemonSet to finish"
            )

        # Clean up the DaemonSet
        code, _ = self._rancher_proxy_request(
            "DELETE", cluster_id,
            f"apis/apps/v1/namespaces/{namespace}/daemonsets/{ds_name}"
        )
        if code not in [200, 204]:
            logging(f"Warning: failed to delete DaemonSet {ds_name}: {code}")
        else:
            logging(f"Deleted DaemonSet {ds_name}")

    def wait_for_chart_app_ready(self, cluster_id, release_name,
                                 namespace, timeout=DEFAULT_TIMEOUT,
                                 expected_version=None):
        """Wait for a chart app to be deployed and ready."""
        logging(f"Waiting for chart app {release_name} to be ready "
                f"in {namespace}"
                + (f" at version {expected_version}" if expected_version else ""))
        retry_count, retry_interval = get_retry_count_and_interval()

        if expected_version:
            time.sleep(5)

        end_time = time.time() + int(timeout)
        iteration = 0
        while time.time() < end_time:
            try:
                code, data = self._rancher_proxy_request(
                    "GET", cluster_id,
                    f"v1/catalog.cattle.io.apps/{namespace}/{release_name}"
                )
                if code == 200 and data:
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

        code, data = self.harvester_api.post(
            "v1/storage.k8s.io.storageclasses",
            data={
                "type": "storage.k8s.io.storageclass",
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
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create RWX StorageClass on host: {code}, {data}"
            )

        logging(f"Created RWX StorageClass on host: {name}")
        return data

    def delete_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Delete the RWX StorageClass from the host Harvester cluster"""
        logging(f"Deleting RWX StorageClass from host: {name}")

        code, data = self.harvester_api.delete(
            f"v1/storage.k8s.io.storageclasses/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(
                f"Failed to delete RWX StorageClass from host: {code}, {data}"
            )

        logging(f"Deleted RWX StorageClass from host: {name}")

    def create_guest_storage_class(self, cluster_id, name,
                                   host_storage_class="longhorn-rwx"):
        """Create a StorageClass on the guest cluster referencing a host SC"""
        logging(f"Creating guest StorageClass {name} in cluster {cluster_id} "
                f"(hostStorageClass={host_storage_class})")

        payload = {
            "type": "storage.k8s.io.storageclass",
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

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/storage.k8s.io.storageclasses",
            payload
        )

        if code not in [200, 201, 409]:
            raise Exception(
                f"Failed to create guest StorageClass: {code}, {data}"
            )

        logging(f"Created guest StorageClass: {name}")
        return data

    def delete_guest_storage_class(self, cluster_id, name):
        """Delete a StorageClass from the guest cluster"""
        logging(f"Deleting guest StorageClass {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "DELETE",
            f"k8s/clusters/{cluster_id}/v1/storage.k8s.io.storageclasses/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(
                f"Failed to delete guest StorageClass: {code}, {data}"
            )

        logging(f"Deleted guest StorageClass: {name}")

    def create_pvc_rwx(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create a ReadWriteMany PVC in guest cluster"""
        logging(f"Creating RWX PVC {name} in cluster {cluster_id}")

        payload = {
            "type": "persistentvolumeclaim",
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
            payload["spec"]["storageClassName"] = storage_class

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/persistentvolumeclaims/{DEFAULT_NAMESPACE}",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create RWX PVC: {code}, {data}")

        logging(f"Created RWX PVC {name}")
        return data

    def create_statefulset(self, cluster_id, namespace, name, image,
                           pvc_name, replicas=2):
        """Create a StatefulSet that mounts an existing PVC"""
        logging(f"Creating StatefulSet {name} in cluster {cluster_id}")

        container = {
            "name": name,
            "image": image,
            "command": ["sh", "-c", "sleep infinity"],
        }

        container["volumeMounts"] = [
            {"name": "shared-data", "mountPath": "/data"}
        ]

        payload = {
            "type": "apps.statefulset",
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

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/apps.statefulsets/{namespace}",
            payload
        )

        if code not in [200, 201]:
            raise Exception(f"Failed to create StatefulSet: {code}, {data}")

        logging(f"Created StatefulSet {name}")
        return data

    def get_statefulset(self, cluster_id, namespace, name):
        """Get StatefulSet details from guest cluster"""
        logging(f"Getting StatefulSet {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "GET",
            f"k8s/clusters/{cluster_id}/v1/apps.statefulsets/{namespace}/{name}"
        )

        if code == 404:
            return None
        elif code != 200:
            raise Exception(f"Failed to get StatefulSet: {code}, {data}")

        return data

    def delete_statefulset(self, cluster_id, namespace, name):
        """Delete StatefulSet from guest cluster"""
        logging(f"Deleting StatefulSet {name} from cluster {cluster_id}")

        code, data = self._rancher_request(
            "DELETE",
            f"k8s/clusters/{cluster_id}/v1/apps.statefulsets/{namespace}/{name}"
        )

        if code not in [200, 204, 404]:
            raise Exception(f"Failed to delete StatefulSet: {code}, {data}")

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
        """Execute a command inside a pod via Rancher proxy"""
        # Kubernetes exec requires a WebSocket/SPDY streaming upgrade
        # which is not supported via plain HTTP Rancher proxy requests.
        raise Exception(
            "REST strategy does not support pod exec via Rancher proxy. "
            "Use CRD strategy (HarvesterOperationStrategy=CRD) or "
            "kubectl exec instead."
        )

    def get_pods_by_label(self, cluster_id, namespace, label_selector):
        """Get pods matching a label selector"""
        logging(f"Getting pods with label {label_selector} in "
                f"cluster {cluster_id}")

        code, data = self._rancher_request(
            "GET",
            f"k8s/clusters/{cluster_id}/v1/pods/{namespace}"
            f"?labelSelector={label_selector}"
        )

        if code != 200:
            raise Exception(f"Failed to get pods: {code}, {data}")

        items = data.get("data", [])
        logging(f"Found {len(items)} pods with label {label_selector}")
        return items

    # Rancher RBAC Operations

    def create_rancher_user(self, user_id, display_name):
        """Create a new Rancher local user via the v3 REST API.

        Args:
            user_id: Username (used as both metadata.name and username field)
            display_name: Human-readable display name

        Returns:
            dict: Created user resource
        """
        logging(f"Creating Rancher user via REST: {user_id}")

        payload = {
            "type": "user",
            "username": user_id,
            "displayName": display_name,
            "enabled": True,
        }

        code, data = self._rancher_request("POST", "v3/users", data=payload)

        if code not in (200, 201):
            raise Exception(
                f"Failed to create Rancher user '{user_id}': {code} {data}"
            )

        logging(f"Created Rancher user: {user_id}")
        return data

    def delete_rancher_user(self, user_id):
        """Delete a Rancher user via the v3 REST API.

        Args:
            user_id: Username to delete
        """
        logging(f"Deleting Rancher user via REST: {user_id}")

        code, data = self._rancher_request("DELETE", f"v3/users/{user_id}")

        if code not in (200, 204, 404):
            raise Exception(
                f"Failed to delete Rancher user '{user_id}': {code} {data}"
            )

        logging(f"Deleted Rancher user: {user_id}")

    def set_user_password(self, user_id, password):
        """Set or update the password for a Rancher local user via the v3 REST API.

        Rancher v3 users support setting the password directly in the user resource.

        Args:
            user_id: Username whose password to set
            password: Plaintext password value
        """
        logging(f"Setting password for Rancher user via REST: {user_id}")

        payload = {"newPassword": password}
        code, data = self._rancher_request(
            "POST",
            f"v3/users/{user_id}?action=setpassword",
            data=payload
        )

        if code not in (200, 201):
            raise Exception(
                f"Failed to set password for user '{user_id}': {code} {data}"
            )

        logging(f"Password set for user: {user_id}")

    def assign_standard_user_role(self, user_id):
        """Assign the Standard User global role via the v3 GlobalRoleBinding API.

        Args:
            user_id: Username to assign the role to
        """
        logging(f"Assigning Standard User role via REST to: {user_id}")

        payload = {
            "type": "globalRoleBinding",
            "globalRoleId": "user",
            "userId": user_id,
        }

        code, data = self._rancher_request(
            "POST", "v3/globalrolebindings", data=payload
        )

        if code not in (200, 201):
            raise Exception(
                f"Failed to assign Standard User role to '{user_id}': "
                f"{code} {data}"
            )

        logging(f"Assigned Standard User role to: {user_id}")

    def create_project(self, cluster_id, display_name):
        """Create a new Rancher project via the v3 REST API.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            display_name: Display name for the project

        Returns:
            str: Short project ID (e.g. p-xxxxx)
        """
        logging(f"Creating project '{display_name}' in cluster {cluster_id} via REST")

        payload = {
            "type": "project",
            "clusterId": cluster_id,
            "name": display_name,
            "containerDefaultResourceLimit": {},
            "namespaceDefaultResourceQuota": {"limit": {}},
            "resourceQuota": {"limit": {}},
        }

        code, data = self._rancher_request("POST", "v3/projects", data=payload)

        if code not in (200, 201):
            raise Exception(
                f"Failed to create project '{display_name}' in cluster '{cluster_id}': "
                f"{code} {data}"
            )

        full_id = data.get("id", "")
        project_id = full_id.split(":")[-1] if ":" in full_id else full_id
        logging(f"Created project '{display_name}': {project_id}")
        return project_id

    def delete_project(self, cluster_id, project_id):
        """Delete a Rancher project via the v3 REST API.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        full_id = f"{cluster_id}:{project_id}"
        logging(f"Deleting project {full_id} via REST")

        code, data = self._rancher_request("DELETE", f"v3/projects/{full_id}")

        if code not in (200, 204):
            raise Exception(
                f"Failed to delete project '{full_id}': {code} {data}"
            )

        logging(f"Deleted project {full_id}")

    def assign_project_role(self, user_id, cluster_id, project_id, role_template_name):
        """Create a ProjectRoleTemplateBinding via the v3 REST API.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-project-view)
        """
        project_name = f"{cluster_id}:{project_id}"
        logging(
            f"Assigning project role '{role_template_name}' to '{user_id}' "
            f"in project '{project_name}' via REST"
        )

        payload = {
            "type": "projectRoleTemplateBinding",
            "projectId": project_name,
            "roleTemplateId": role_template_name,
            "userId": user_id,
        }

        code, data = self._rancher_request(
            "POST", "v3/projectroletemplatebindings", data=payload
        )

        if code not in (200, 201):
            raise Exception(
                f"Failed to assign project role '{role_template_name}' "
                f"to '{user_id}': {code} {data}"
            )

        logging(
            f"Assigned project role '{role_template_name}' to user '{user_id}'"
        )

    def delete_user_global_role_bindings(self, user_id):
        """Delete all GlobalRoleBindings owned by the given user via REST.

        Args:
            user_id: Username whose GlobalRoleBindings to remove
        """
        logging(f"Deleting GlobalRoleBindings via REST for user: {user_id}")

        code, data = self._rancher_request(
            "GET", f"v3/globalrolebindings?userId={user_id}"
        )

        if code != 200:
            logging(
                f"Warning: failed to list GlobalRoleBindings: {code} {data}",
                level="WARNING"
            )
            return

        deleted = 0
        for item in data.get("data", []):
            binding_id = item.get("id", "")
            del_code, _ = self._rancher_request(
                "DELETE", f"v3/globalrolebindings/{binding_id}"
            )
            if del_code not in (200, 204):
                logging(
                    f"Warning: failed to delete GRB '{binding_id}'",
                    level="WARNING"
                )
            else:
                deleted += 1

        logging(f"Deleted {deleted} GlobalRoleBinding(s) for user '{user_id}'")

    def delete_user_project_role_bindings(self, user_id, cluster_id, project_id):
        """Delete all ProjectRoleTemplateBindings owned by the user via REST.

        Args:
            user_id: Username whose bindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        project_name = f"{cluster_id}:{project_id}"
        logging(
            f"Deleting PRTBs via REST for user '{user_id}' "
            f"in project '{project_name}'"
        )

        code, data = self._rancher_request(
            "GET",
            f"v3/projectroletemplatebindings"
            f"?projectId={project_name}&userId={user_id}"
        )

        if code != 200:
            logging(
                f"Warning: failed to list PRTBs: {code} {data}",
                level="WARNING"
            )
            return

        deleted = 0
        for item in data.get("data", []):
            binding_id = item.get("id", "")
            del_code, _ = self._rancher_request(
                "DELETE", f"v3/projectroletemplatebindings/{binding_id}"
            )
            if del_code not in (200, 204):
                logging(
                    f"Warning: failed to delete PRTB '{binding_id}'",
                    level="WARNING"
                )
            else:
                deleted += 1

        logging(
            f"Deleted {deleted} ProjectRoleTemplateBinding(s) for user '{user_id}'"
        )

    def assign_cluster_role(self, user_id, cluster_id, role_template_name):
        """Create a ClusterRoleTemplateBinding via the v3 REST API.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-cluster-view)
        """
        logging(
            f"Assigning cluster role '{role_template_name}' to '{user_id}' "
            f"in cluster '{cluster_id}' via REST"
        )

        payload = {
            "type": "clusterRoleTemplateBinding",
            "clusterId": cluster_id,
            "roleTemplateId": role_template_name,
            "userId": user_id,
        }

        code, data = self._rancher_request(
            "POST", "v3/clusterroletemplatebindings", data=payload
        )

        if code not in (200, 201):
            raise Exception(
                f"Failed to assign cluster role '{role_template_name}' "
                f"to '{user_id}': {code} {data}"
            )

        logging(
            f"Assigned cluster role '{role_template_name}' to user '{user_id}'"
        )

    def delete_user_cluster_role_bindings(self, user_id, cluster_id):
        """Delete all ClusterRoleTemplateBindings for the given user via REST.

        Args:
            user_id: Username whose ClusterRoleTemplateBindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        logging(
            f"Deleting CRTBs via REST for user '{user_id}' "
            f"in cluster '{cluster_id}'"
        )

        code, data = self._rancher_request(
            "GET",
            f"v3/clusterroletemplatebindings"
            f"?clusterId={cluster_id}&userId={user_id}"
        )

        if code != 200:
            logging(
                f"Warning: failed to list CRTBs: {code} {data}",
                level="WARNING"
            )
            return

        deleted = 0
        for item in data.get("data", []):
            binding_id = item.get("id", "")
            del_code, _ = self._rancher_request(
                "DELETE", f"v3/clusterroletemplatebindings/{binding_id}"
            )
            if del_code not in (200, 204):
                logging(
                    f"Warning: failed to delete CRTB '{binding_id}'",
                    level="WARNING"
                )
            else:
                deleted += 1

        logging(
            f"Deleted {deleted} ClusterRoleTemplateBinding(s) for user '{user_id}'"
        )

    def _run_kubectl_with_content(self, kubeconfig_content, args):
        """Run kubectl using an in-memory kubeconfig string."""
        import subprocess
        import tempfile as _tempfile
        fd, path = _tempfile.mkstemp(suffix=".kubeconfig")
        try:
            with os.fdopen(fd, 'w') as f:
                f.write(kubeconfig_content)
            cmd = ["kubectl", "--kubeconfig", path,
                   "--insecure-skip-tls-verify"] + list(args)
            result = subprocess.run(cmd, capture_output=True, text=True)  # nosec B603
            return result.returncode, result.stdout, result.stderr
        finally:
            if os.path.exists(path):
                os.remove(path)

    def generate_user_kubeconfig(self, user_id, password, cluster_id):
        """Login as user_id and return a kubeconfig YAML string for cluster_id."""
        self._authenticate_rancher()  # ensure self.rancher_endpoint is set
        endpoint = self.rancher_endpoint
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

        code, data = self._rancher_request(
            "POST",
            f"k8s/clusters/{cluster_id}/v1/namespaces",
            data={
                "type": "namespace",
                "metadata": {
                    "name": namespace_name,
                    "annotations": {
                        "field.cattle.io/projectId": project_ref
                    },
                    "labels": {
                        "field.cattle.io/projectId": project_ref
                    }
                }
            }
        )
        if code not in (200, 201):
            raise Exception(
                f"Failed to create namespace '{namespace_name}': {code} {data}"
            )
        logging(f"Namespace '{namespace_name}' created and assigned to project")
        return data

    def delete_namespace_from_cluster(self, namespace_name, cluster_id):
        """Delete a namespace from the Harvester cluster."""
        logging(f"Deleting namespace '{namespace_name}'")
        code, _ = self._rancher_request(
            "DELETE",
            f"k8s/clusters/{cluster_id}/v1/namespaces/{namespace_name}"
        )
        if code not in (200, 204, 404):
            logging(
                f"Warning: failed to delete namespace '{namespace_name}': {code}",
                level="WARNING"
            )
