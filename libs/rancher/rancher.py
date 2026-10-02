"""
Rancher Component - delegates to CRD or REST implementation
Layer 4: Selects implementation based on strategy

The implementation is selected based on the HARVESTER_OPERATION_STRATEGY
environment variable. Valid values are 'crd' or 'rest'. Defaults to 'crd' if not set.
"""

import os
from constant import HarvesterOperationStrategy
from rancher.rest import Rest
from rancher.crd import CRD
from rancher.base import Base


class Rancher(Base):
    """
    Rancher component that delegates to CRD or REST implementation

    The implementation is selected based on:
    - HARVESTER_OPERATION_STRATEGY environment variable ('crd' or 'rest')
    - Defaults to 'crd' if not set
    """

    def __init__(self):
        """Initialize Rancher component"""
        # Get strategy from environment variable, default to CRD
        strategy_str = os.getenv("HARVESTER_OPERATION_STRATEGY", "crd").lower()
        try:
            self._strategy = HarvesterOperationStrategy(strategy_str)
        except ValueError:
            # If invalid value, default to CRD
            self._strategy = HarvesterOperationStrategy.CRD

        if self._strategy == HarvesterOperationStrategy.CRD:
            self.rancher = CRD()
        else:
            self.rancher = Rest()

        # Lazily created only if the active strategy isn't already Rest -
        # see install_chart(), which always uses Rest regardless of strategy.
        self._chart_rest_instance = None

    def _chart_rest(self):
        """Return a Rest() instance for chart operations, reusing self.rancher
        if it's already a Rest instance to avoid creating a redundant one."""
        if isinstance(self.rancher, Rest):
            return self.rancher
        if self._chart_rest_instance is None:
            self._chart_rest_instance = Rest()
        return self._chart_rest_instance

    # Harvester Management Cluster Operations
    def create_harvester_mgmt_cluster(self, cluster_name):
        """Create Harvester management cluster entry in Rancher (Import Existing)"""
        return self.rancher.create_harvester_mgmt_cluster(cluster_name)

    def get_harvester_mgmt_cluster(self, cluster_name):
        """Get Harvester management cluster details"""
        return self.rancher.get_harvester_mgmt_cluster(cluster_name)

    def delete_harvester_mgmt_cluster(self, cluster_name):
        """Delete Harvester management cluster entry"""
        return self.rancher.delete_harvester_mgmt_cluster(cluster_name)

    def wait_for_cluster_id(self, cluster_name, timeout):
        """Wait for cluster to get its internal ID"""
        return self.rancher.wait_for_cluster_id(cluster_name, timeout)

    def wait_for_harvester_ready(self, cluster_name, timeout):
        """Wait for Harvester cluster to be ready in Rancher"""
        return self.rancher.wait_for_harvester_ready(cluster_name, timeout)

    # Cluster Registration Operations
    def get_cluster_registration_url(self, cluster_id, rancher_endpoint, timeout=300):
        """Get cluster registration URL for importing Harvester"""
        return self.rancher.get_cluster_registration_url(cluster_id, rancher_endpoint, timeout)

    def set_cluster_registration_url(self, url):
        """Set cluster-registration-url setting in Harvester"""
        return self.rancher.set_cluster_registration_url(url)

    def get_all_rke2_versions(self, rancher_endpoint=None, max_versions=None):
        """Get all available RKE2 versions from Rancher"""
        return self.rancher.get_all_rke2_versions(rancher_endpoint, max_versions)

    def get_rke2_version(self, target_version, rancher_endpoint=None):
        """Get RKE2 version from Rancher that matches target version"""
        return self.rancher.get_rke2_version(target_version, rancher_endpoint)

    def configure_kdm_url(self, url):
        """Update Rancher global rke-metadata-config to use a custom KDM URL"""
        return self.rancher.configure_kdm_url(url)

    def get_harvester_node_driver_version(self):
        """Get the Harvester node driver (docker-machine-driver-harvester) version"""
        return self.rancher.get_harvester_node_driver_version()

    # Cloud Credential Operations
    def create_cloud_credential(self, name, kubeconfig, cluster_id):
        """Create cloud credential for Harvester"""
        return self.rancher.create_cloud_credential(name, kubeconfig, cluster_id)

    def get_cloud_credential(self, credential_id):
        """Get cloud credential details"""
        return self.rancher.get_cloud_credential(credential_id)

    def delete_cloud_credential(self, credential_id):
        """Delete cloud credential"""
        return self.rancher.delete_cloud_credential(credential_id)

    # RKE2 Cluster Operations
    def create_rke2_cluster(self, name, cloud_provider_config_id, hostname_prefix,
                            harvester_config_name, k8s_version, cloud_credential_id,
                            quantity, ingress):
        """Create RKE2 cluster on Harvester"""
        return self.rancher.create_rke2_cluster(
            name, cloud_provider_config_id, hostname_prefix,
            harvester_config_name, k8s_version, cloud_credential_id,
            quantity, ingress
        )

    def get_rke2_cluster(self, cluster_name):
        """Get RKE2 cluster details"""
        return self.rancher.get_rke2_cluster(cluster_name)

    def delete_rke2_cluster(self, cluster_name):
        """Delete RKE2 cluster"""
        return self.rancher.delete_rke2_cluster(cluster_name)

    def wait_for_rke2_cluster_ready(self, cluster_name, timeout):
        """Wait for RKE2 cluster to be ready"""
        return self.rancher.wait_for_rke2_cluster_ready(cluster_name, timeout)

    def wait_for_rke2_cluster_deleted(self, cluster_name, timeout):
        """Wait for RKE2 cluster to be deleted"""
        return self.rancher.wait_for_rke2_cluster_deleted(cluster_name, timeout)

    def scale_rke2_cluster(self, cluster_name, worker_count, harvester_config_name):
        """Scale RKE2 cluster by adding/removing a worker-only machine pool"""
        return self.rancher.scale_rke2_cluster(cluster_name, worker_count, harvester_config_name)

    def upgrade_rke2_cluster(self, cluster_name, new_k8s_version):
        """Upgrade RKE2 cluster to a new Kubernetes version"""
        return self.rancher.upgrade_rke2_cluster(cluster_name, new_k8s_version)

    # Harvester Config Operations
    def create_harvester_config(self, name, cpus, mems, disks, image_id,
                                network_id, ssh_user, user_data):
        """Create Harvester config for RKE2 node template"""
        return self.rancher.create_harvester_config(
            name, cpus, mems, disks, image_id, network_id, ssh_user, user_data
        )

    # Kubeconfig Operations
    def generate_kubeconfig(self, cluster_id, cluster_name):
        """Generate full-access kubeconfig for a cluster"""
        return self.rancher.generate_kubeconfig(cluster_id, cluster_name)

    def generate_cloud_provider_kubeconfig(self, cluster_id, cluster_name):
        """Generate cloud provider kubeconfig with external URL"""
        return self.rancher.generate_cloud_provider_kubeconfig(cluster_id, cluster_name)

    # Secret Operations
    def create_secret(self, name, data, annotations):
        """Create secret for cloud provider config"""
        return self.rancher.create_secret(name, data, annotations)

    # Deployment Operations
    def create_deployment(self, cluster_id, namespace, name, image, pvc=None):
        """Create deployment in guest cluster"""
        return self.rancher.create_deployment(cluster_id, namespace, name,
                                              image, pvc)

    def get_deployment(self, cluster_id, namespace, name):
        """Get deployment details"""
        return self.rancher.get_deployment(cluster_id, namespace, name)

    def delete_deployment(self, cluster_id, namespace, name):
        """Delete deployment"""
        return self.rancher.delete_deployment(cluster_id, namespace, name)

    def wait_for_deployment_ready(self, cluster_id, namespace, name, timeout):
        """Wait for deployment to be ready"""
        return self.rancher.wait_for_deployment_ready(cluster_id, namespace, name, timeout)

    def wait_for_deployment_deleted(self, cluster_id, namespace, name, timeout):
        """Wait for deployment to be deleted"""
        return self.rancher.wait_for_deployment_deleted(cluster_id, namespace, name, timeout)

    def scale_deployment(self, cluster_id, namespace, name, replicas):
        """Scale a deployment to the given replica count"""
        return self.rancher.scale_deployment(
            cluster_id, namespace, name, replicas
        )

    def wait_for_deployment_scaled(self, cluster_id, namespace, name,
                                   replicas, timeout):
        """Wait for a deployment to reach the given ready replica count"""
        return self.rancher.wait_for_deployment_scaled(
            cluster_id, namespace, name, replicas, timeout
        )

    # PVC Operations
    def create_pvc(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create PVC in guest cluster"""
        return self.rancher.create_pvc(cluster_id, name, size, storage_class)

    def get_pvc(self, cluster_id, name):
        """Get PVC details"""
        return self.rancher.get_pvc(cluster_id, name)

    def delete_pvc(self, cluster_id, name):
        """Delete PVC"""
        return self.rancher.delete_pvc(cluster_id, name)

    def wait_for_pvc_bound(self, cluster_id, name, timeout):
        """Wait for PVC to be bound"""
        return self.rancher.wait_for_pvc_bound(cluster_id, name, timeout)

    # Load Balancer Service Operations
    def create_lb_service(self, cluster_id, service_data):
        """Create LoadBalancer service"""
        return self.rancher.create_lb_service(cluster_id, service_data)

    def get_lb_service(self, cluster_id, name):
        """Get LoadBalancer service details"""
        return self.rancher.get_lb_service(cluster_id, name)

    def delete_lb_service(self, cluster_id, name):
        """Delete LoadBalancer service"""
        return self.rancher.delete_lb_service(cluster_id, name)

    def wait_for_lb_service_ready(self, cluster_id, name, timeout):
        """Wait for LoadBalancer service to be ready"""
        return self.rancher.wait_for_lb_service_ready(cluster_id, name, timeout)

    def query_lb_service(self, url, retries=10, interval=5):
        """Query LoadBalancer service endpoint with retries"""
        return self.rancher.query_lb_service(url, retries, interval)

    def query_lb_via_proxy(self, cluster_id, service_name, port=8080,
                           namespace="default", retries=10, interval=5):
        """Query LoadBalancer service via Rancher's k8s service proxy"""
        return self.rancher.query_lb_via_proxy(
            cluster_id, service_name, port, namespace, retries, interval)

    # Harvester Deployments Check
    def wait_for_harvester_deployments_ready(self, cluster_id, timeout):
        """Wait for harvester-cloud-provider and harvester-csi-driver to be ready"""
        return self.rancher.wait_for_harvester_deployments_ready(cluster_id, timeout)

    # Import Existing Cluster Operations
    def create_import_cluster(self, name):
        """Create a minimal provisioning cluster for import"""
        return self.rancher.create_import_cluster(name)

    def wait_for_import_cluster_ready(self, cluster_name, timeout):
        """Wait for an imported cluster to become active"""
        return self.rancher.wait_for_import_cluster_ready(
            cluster_name, timeout
        )

    # Custom RKE2 Cluster Operations
    def create_custom_rke2_cluster(self, name, cloud_provider_config_id,
                                   k8s_version, cloud_credential_id,
                                   ingress="traefik"):
        """Create a custom RKE2 cluster without machinePools"""
        return self.rancher.create_custom_rke2_cluster(
            name, cloud_provider_config_id, k8s_version,
            cloud_credential_id, ingress
        )

    def update_cluster_chart_name(self, cluster_name, mgmt_cluster_id):
        """Patch custom cluster's chartValues with the real management ID"""
        return self.rancher.update_cluster_chart_name(
            cluster_name, mgmt_cluster_id
        )

    def fix_cloud_provider_cluster_name(self, cluster_id):
        """Fix cloud-provider --cluster-name arg on the guest cluster"""
        return self.rancher.fix_cloud_provider_cluster_name(cluster_id)

    def get_cluster_registration_command(self, cluster_name, timeout):
        """Get the node registration command for a custom cluster"""
        return self.rancher.get_cluster_registration_command(
            cluster_name, timeout
        )

    # Harvester VM Operations (for custom cluster nodes)
    def create_harvester_vm(self, name, image_id, network_id, cpus, memory,
                            disk_size, ssh_user, user_data, network_data="",
                            guest_cluster_id=""):
        """Create a VM on Harvester"""
        return self.rancher.create_harvester_vm(
            name, image_id, network_id, cpus, memory,
            disk_size, ssh_user, user_data, network_data,
            guest_cluster_id
        )

    def wait_for_harvester_vm_ready(self, name, timeout):
        """Wait for a Harvester VM to be running"""
        return self.rancher.wait_for_harvester_vm_ready(name, timeout)

    def delete_harvester_vm(self, name):
        """Delete a Harvester VM"""
        return self.rancher.delete_harvester_vm(name)

    # Chart Install Operations
    def install_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Install a Helm chart on a guest cluster.

        Always uses the REST implementation regardless of
        HARVESTER_OPERATION_STRATEGY: crd.py's path to Rancher's catalog
        install action has shown recurring "lost connection to cluster"
        tunnel failures on this environment that rest.py's direct
        session-based API calls do not.
        """
        return self._chart_rest().install_chart(
            cluster_id, repo_name, chart_name, version,
            release_name, namespace, values
        )

    def upgrade_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Upgrade an installed Helm chart on a guest cluster"""
        return self.rancher.upgrade_chart(
            cluster_id, repo_name, chart_name, version,
            release_name, namespace, values
        )

    def uninstall_chart(self, cluster_id, release_name, namespace):
        """Uninstall a Helm chart from a guest cluster"""
        return self.rancher.uninstall_chart(
            cluster_id, release_name, namespace
        )

    def wait_for_chart_app_deleted(self, cluster_id, release_name,
                                   namespace, timeout):
        """Wait for a chart app to be fully removed"""
        return self.rancher.wait_for_chart_app_deleted(
            cluster_id, release_name, namespace, timeout
        )

    def create_cluster_repo(self, cluster_id, repo_name, git_url, git_branch):
        """Create a ClusterRepo on a guest cluster"""
        return self.rancher.create_cluster_repo(
            cluster_id, repo_name, git_url, git_branch
        )

    def wait_for_cluster_repo_ready(self, cluster_id, repo_name,
                                    timeout=600, expected_git_branch=None):
        """Wait for a ClusterRepo to finish downloading"""
        return self.rancher.wait_for_cluster_repo_ready(
            cluster_id, repo_name, timeout, expected_git_branch
        )

    def get_chart_versions(self, repo_name, chart_name, cluster_id=None):
        """Get available versions for a chart"""
        return self.rancher.get_chart_versions(
            repo_name, chart_name, cluster_id
        )

    def get_deployed_chart_version(self, cluster_id, release_name, namespace):
        """Return the deployed version of an installed chart app"""
        return self.rancher.get_deployed_chart_version(
            cluster_id, release_name, namespace
        )

    def create_cloud_config_secret(self, cluster_id, secret_name,
                                   namespace, kubeconfig):
        """Create cloud-provider-config secret on guest cluster"""
        return self.rancher.create_cloud_config_secret(
            cluster_id, secret_name, namespace, kubeconfig
        )

    def write_cloud_config_to_nodes(self, cluster_id, secret_name, namespace):
        """Write cloud-provider-config to each node hostPath via a DaemonSet"""
        return self.rancher.write_cloud_config_to_nodes(
            cluster_id, secret_name, namespace
        )

    def wait_for_chart_app_ready(self, cluster_id, release_name,
                                 namespace, timeout, expected_version=None):
        """Wait for chart app to be deployed"""
        return self.rancher.wait_for_chart_app_ready(
            cluster_id, release_name, namespace, timeout, expected_version
        )

    # RWX Volume / StorageClass / StatefulSet Operations
    def create_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Create an RWX-capable StorageClass on the host Harvester cluster"""
        return self.rancher.create_rwx_storage_class_on_host(name)

    def delete_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Delete the RWX StorageClass from the host Harvester cluster"""
        return self.rancher.delete_rwx_storage_class_on_host(name)

    def create_guest_storage_class(self, cluster_id, name,
                                   host_storage_class="longhorn-rwx"):
        """Create a StorageClass on the guest cluster referencing a host SC"""
        return self.rancher.create_guest_storage_class(
            cluster_id, name, host_storage_class
        )

    def delete_guest_storage_class(self, cluster_id, name):
        """Delete a StorageClass from the guest cluster"""
        return self.rancher.delete_guest_storage_class(cluster_id, name)

    def create_pvc_rwx(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create a ReadWriteMany PVC in guest cluster"""
        return self.rancher.create_pvc_rwx(cluster_id, name, size, storage_class)

    def create_statefulset(self, cluster_id, namespace, name, image,
                           pvc_name, replicas=2):
        """Create a StatefulSet that mounts an existing PVC"""
        return self.rancher.create_statefulset(
            cluster_id, namespace, name, image, pvc_name, replicas
        )

    def get_statefulset(self, cluster_id, namespace, name):
        """Get StatefulSet details"""
        return self.rancher.get_statefulset(cluster_id, namespace, name)

    def delete_statefulset(self, cluster_id, namespace, name):
        """Delete StatefulSet"""
        return self.rancher.delete_statefulset(cluster_id, namespace, name)

    def wait_for_statefulset_ready(self, cluster_id, namespace, name, timeout):
        """Wait for StatefulSet to have all replicas ready"""
        return self.rancher.wait_for_statefulset_ready(
            cluster_id, namespace, name, timeout
        )

    def exec_command_in_pod(self, cluster_id, namespace, pod_name, command):
        """Execute a command inside a pod"""
        return self.rancher.exec_command_in_pod(
            cluster_id, namespace, pod_name, command
        )

    def get_pods_by_label(self, cluster_id, namespace, label_selector):
        """Get pods matching a label selector"""
        return self.rancher.get_pods_by_label(
            cluster_id, namespace, label_selector
        )

    # Rancher RBAC Operations

    def create_rancher_user(self, user_id, display_name):
        """Create a new Rancher local user"""
        return self.rancher.create_rancher_user(user_id, display_name)

    def delete_rancher_user(self, user_id):
        """Delete a Rancher user and its associated password secret"""
        return self.rancher.delete_rancher_user(user_id)

    def set_user_password(self, user_id, password):
        """Create or update the password for a Rancher local user"""
        return self.rancher.set_user_password(user_id, password)

    def assign_standard_user_role(self, user_id):
        """Assign the Standard User global role to a user"""
        return self.rancher.assign_standard_user_role(user_id)

    def create_project(self, cluster_id, display_name):
        """Create a new Rancher project and return its short project ID"""
        return self.rancher.create_project(cluster_id, display_name)

    def delete_project(self, cluster_id, project_id):
        """Delete a Rancher project"""
        return self.rancher.delete_project(cluster_id, project_id)

    def assign_project_role(self, user_id, cluster_id, project_id, role_template_name):
        """Create a ProjectRoleTemplateBinding to grant a user a project-scoped role"""
        return self.rancher.assign_project_role(
            user_id, cluster_id, project_id, role_template_name
        )

    def delete_user_global_role_bindings(self, user_id):
        """Delete all GlobalRoleBindings owned by the given user"""
        return self.rancher.delete_user_global_role_bindings(user_id)

    def delete_user_project_role_bindings(self, user_id, cluster_id, project_id):
        """Delete all ProjectRoleTemplateBindings owned by the user in a project"""
        return self.rancher.delete_user_project_role_bindings(
            user_id, cluster_id, project_id
        )

    def assign_cluster_role(self, user_id, cluster_id, role_template_name):
        """Create a ClusterRoleTemplateBinding to grant a user a cluster-scoped role"""
        return self.rancher.assign_cluster_role(user_id, cluster_id, role_template_name)

    def delete_user_cluster_role_bindings(self, user_id, cluster_id):
        """Delete all ClusterRoleTemplateBindings owned by the given user in a cluster"""
        return self.rancher.delete_user_cluster_role_bindings(user_id, cluster_id)

    def generate_user_kubeconfig(self, user_id, password, cluster_id):
        """Login as user_id and return a kubeconfig YAML string for cluster_id"""
        return self.rancher.generate_user_kubeconfig(user_id, password, cluster_id)

    def verify_resource_access(self, kubeconfig_content, verb, resource, namespace):
        """Run kubectl auth can-i <verb> <resource> -n <namespace> and return (ok, output)"""
        return self.rancher.verify_resource_access(
            kubeconfig_content, verb, resource, namespace
        )

    def create_namespace_in_project(self, namespace_name, cluster_id, project_id):
        """Create namespace in Harvester cluster and assign to a Rancher project"""
        return self.rancher.create_namespace_in_project(
            namespace_name, cluster_id, project_id
        )

    def delete_namespace_from_cluster(self, namespace_name, cluster_id):
        """Delete a namespace from the Harvester cluster"""
        return self.rancher.delete_namespace_from_cluster(namespace_name, cluster_id)
