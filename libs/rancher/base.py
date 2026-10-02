"""
Base class for Rancher Integration operations
Defines the interface for both CRD and REST implementations
"""
from abc import ABC, abstractmethod


class Base(ABC):
    """Base class for Rancher Integration implementations"""

    # Harvester Management Cluster Operations
    @abstractmethod
    def create_harvester_mgmt_cluster(self, cluster_name):
        """Create Harvester management cluster entry in Rancher (Import Existing)"""
        pass

    @abstractmethod
    def get_harvester_mgmt_cluster(self, cluster_name):
        """Get Harvester management cluster details"""
        pass

    @abstractmethod
    def delete_harvester_mgmt_cluster(self, cluster_name):
        """Delete Harvester management cluster entry"""
        pass

    @abstractmethod
    def wait_for_cluster_id(self, cluster_name, timeout):
        """
        Wait for cluster to get its internal ID (status.clusterName).
        This appears shortly after cluster creation, before registration.
        """
        pass

    @abstractmethod
    def wait_for_harvester_ready(self, cluster_name, timeout):
        """
        Wait for Harvester cluster to be active and ready in Rancher.
        This happens after setting the registration URL.
        """
        pass

    # Cluster Registration Operations
    @abstractmethod
    def get_cluster_registration_url(self, cluster_id, rancher_endpoint, timeout=300):
        """Get cluster registration URL for importing Harvester"""
        pass

    @abstractmethod
    def set_cluster_registration_url(self, url):
        """Set cluster-registration-url setting in Harvester"""
        pass

    # Version Operations
    @abstractmethod
    def get_all_rke2_versions(self, rancher_endpoint, max_versions=None):
        """
        Get all available RKE2 versions from Rancher.

        Args:
            rancher_endpoint: Rancher server endpoint
            max_versions: Maximum number of versions to return (None = all)

        Returns:
            List of version strings sorted by semantic version (newest first)
        """
        pass

    @abstractmethod
    def get_rke2_version(self, target_version):
        """
        Get RKE2 version from Rancher that matches target version.

        Args:
            target_version: Target version prefix (e.g. 'v1.28', 'v1.29')

        Returns:
            Full version string (e.g. 'v1.28.15+rke2r1')
        """
        pass

    @abstractmethod
    def get_harvester_node_driver_version(self):
        """
        Get the Harvester node driver (docker-machine-driver-harvester) version.

        Returns:
            str: Version string (e.g. '1.0.6')
        """
        pass

    @abstractmethod
    def configure_kdm_url(self, url):
        """
        Update the Rancher global setting rke-metadata-config to use a custom KDM URL.

        Sets url to the provided value and refresh-interval-minutes to 0 so Rancher fetches
        the new data.json immediately.

        Args:
            url: URL of the custom KDM data.json file
        """
        pass

    # Cloud Credential Operations
    @abstractmethod
    def create_cloud_credential(self, name, kubeconfig, cluster_id):
        """Create cloud credential for Harvester"""
        pass

    @abstractmethod
    def get_cloud_credential(self, credential_id):
        """Get cloud credential details"""
        pass

    @abstractmethod
    def delete_cloud_credential(self, credential_id):
        """Delete cloud credential"""
        pass

    # RKE2 Cluster Operations
    @abstractmethod
    def create_rke2_cluster(self, name, cloud_provider_config_id, hostname_prefix,
                            harvester_config_name, k8s_version, cloud_credential_id,
                            quantity, ingress):
        """Create RKE2 cluster on Harvester"""
        pass

    @abstractmethod
    def get_rke2_cluster(self, cluster_name):
        """Get RKE2 cluster details"""
        pass

    @abstractmethod
    def delete_rke2_cluster(self, cluster_name):
        """Delete RKE2 cluster"""
        pass

    @abstractmethod
    def wait_for_rke2_cluster_ready(self, cluster_name, timeout):
        """Wait for RKE2 cluster to be ready"""
        pass

    @abstractmethod
    def wait_for_rke2_cluster_deleted(self, cluster_name, timeout):
        """Wait for RKE2 cluster to be deleted"""
        pass

    @abstractmethod
    def scale_rke2_cluster(self, cluster_name, worker_count, harvester_config_name):
        """Scale RKE2 cluster by adding/removing a worker-only machine pool.

        Args:
            cluster_name: Name of the RKE2 cluster
            worker_count: Number of worker nodes (0 to remove worker pool)
            harvester_config_name: HarvesterConfig name for worker pool nodes
        """
        pass

    @abstractmethod
    def upgrade_rke2_cluster(self, cluster_name, new_k8s_version):
        """Upgrade RKE2 cluster to a new Kubernetes version.

        Args:
            cluster_name: Name of the RKE2 cluster
            new_k8s_version: Target Kubernetes version string
        """
        pass

    # Harvester Config Operations
    @abstractmethod
    def create_harvester_config(self, name, cpus, mems, disks, image_id,
                                network_id, ssh_user, user_data):
        """Create Harvester config for RKE2 node template"""
        pass

    # Kubeconfig Operations
    @abstractmethod
    def generate_kubeconfig(self, cluster_id, cluster_name):
        """Generate full-access kubeconfig for a cluster"""
        pass

    @abstractmethod
    def generate_cloud_provider_kubeconfig(self, cluster_id, cluster_name):
        """Generate cloud provider kubeconfig with external URL"""
        pass

    # Secret Operations
    @abstractmethod
    def create_secret(self, name, data, annotations):
        """Create secret for cloud provider config"""
        pass

    # Deployment Operations
    @abstractmethod
    def create_deployment(self, cluster_id, namespace, name, image, pvc=None):
        """Create deployment in guest cluster"""
        pass

    @abstractmethod
    def get_deployment(self, cluster_id, namespace, name):
        """Get deployment details"""
        pass

    @abstractmethod
    def delete_deployment(self, cluster_id, namespace, name):
        """Delete deployment"""
        pass

    @abstractmethod
    def wait_for_deployment_ready(self, cluster_id, namespace, name, timeout):
        """Wait for deployment to be ready"""
        pass

    @abstractmethod
    def wait_for_deployment_deleted(self, cluster_id, namespace, name, timeout):
        """Wait for deployment to be deleted"""
        pass

    @abstractmethod
    def scale_deployment(self, cluster_id, namespace, name, replicas):
        """Scale a deployment in a guest cluster to the given replica count"""
        pass

    @abstractmethod
    def wait_for_deployment_scaled(self, cluster_id, namespace, name,
                                   replicas, timeout):
        """Wait for a deployment to reach the given ready replica count"""
        pass

    # PVC Operations
    @abstractmethod
    def create_pvc(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create PVC in guest cluster"""
        pass

    @abstractmethod
    def get_pvc(self, cluster_id, name):
        """Get PVC details"""
        pass

    @abstractmethod
    def delete_pvc(self, cluster_id, name):
        """Delete PVC"""
        pass

    @abstractmethod
    def wait_for_pvc_bound(self, cluster_id, name, timeout):
        """Wait for PVC to be bound"""
        pass

    # Load Balancer Service Operations
    @abstractmethod
    def create_lb_service(self, cluster_id, service_data):
        """Create LoadBalancer service"""
        pass

    @abstractmethod
    def get_lb_service(self, cluster_id, name):
        """Get LoadBalancer service details"""
        pass

    @abstractmethod
    def delete_lb_service(self, cluster_id, name):
        """Delete LoadBalancer service"""
        pass

    @abstractmethod
    def wait_for_lb_service_ready(self, cluster_id, name, timeout):
        """Wait for LoadBalancer service to be ready"""
        pass

    @abstractmethod
    def query_lb_service(self, url, retries=10, interval=5):
        """Query LoadBalancer service endpoint with retries"""
        pass

    @abstractmethod
    def query_lb_via_proxy(self, cluster_id, service_name, port=8080,
                           namespace="default", retries=10, interval=5):
        """Query LoadBalancer service via Rancher's k8s service proxy"""
        pass

    # Harvester Deployments Check
    @abstractmethod
    def wait_for_harvester_deployments_ready(self, cluster_id, timeout):
        """Wait for harvester-cloud-provider and harvester-csi-driver to be ready"""
        pass

    # Import Existing Cluster Operations
    @abstractmethod
    def create_import_cluster(self, name):
        """Create a minimal provisioning cluster for importing an existing cluster.

        Unlike RKE2 provisioning or custom clusters, this creates a cluster
        with an empty spec. The actual cluster is running externally and the
        Rancher agent is applied to it via the registration manifest URL.

        Args:
            name: Cluster name
        """
        pass

    @abstractmethod
    def wait_for_import_cluster_ready(self, cluster_name, timeout):
        """Wait for an imported cluster to become active in Rancher.

        Checks status.ready and Updated/Provisioned conditions.
        Unlike wait_for_rke2_cluster_ready, this does NOT check
        machinePools or cluster.x-k8s.io machines (imported clusters
        have none).

        Args:
            cluster_name: Name of the provisioning cluster
            timeout: Timeout in seconds
        """
        pass

    # Custom RKE2 Cluster Operations
    @abstractmethod
    def create_custom_rke2_cluster(self, name, cloud_provider_config_id,
                                   k8s_version, cloud_credential_id,
                                   ingress="traefik"):
        """Create a custom RKE2 cluster without machinePools.

        The cluster uses the Harvester cloud provider but nodes are
        registered externally via the registration command.

        Args:
            name: Cluster name
            cloud_provider_config_id: Cloud provider config secret ID
            k8s_version: Kubernetes version
            cloud_credential_id: Cloud credential ID
            ingress: Ingress controller (default: traefik)
        """
        pass

    @abstractmethod
    def update_cluster_chart_name(self, cluster_name, mgmt_cluster_id):
        """Update the cloud provider clusterName in a custom cluster's chartValues.

        For custom clusters, the management cluster ID (c-m-xxxxx) is not
        known at creation time. This patches the provisioning cluster spec
        so the cloud provider can correctly identify VMs belonging to this
        cluster.

        Args:
            cluster_name: Provisioning cluster name
            mgmt_cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        pass

    @abstractmethod
    def fix_cloud_provider_cluster_name(self, cluster_id):
        """Fix the cloud-provider --cluster-name arg on the guest cluster.

        Rancher deploys the chart with the provisioning name instead of
        the management cluster ID. This patches the deployment's arg
        directly and waits for the rollout to complete.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        pass

    @abstractmethod
    def get_cluster_registration_command(self, cluster_name, timeout):
        """Get the node registration command for a custom cluster.

        Args:
            cluster_name: Name of the provisioning cluster
            timeout: Timeout in seconds

        Returns:
            str: The insecure node command to run on VMs
        """
        pass

    # Harvester VM Operations (for custom cluster nodes)
    @abstractmethod
    def create_harvester_vm(self, name, image_id, network_id, cpus, memory,
                            disk_size, ssh_user, user_data, network_data="",
                            guest_cluster_id=""):
        """Create a VM on Harvester.

        Args:
            name: VM name
            image_id: Image ID (namespace/name format)
            network_id: Network ID (namespace/name format)
            cpus: Number of CPU cores
            memory: Memory size in GiB
            disk_size: Disk size in GiB
            ssh_user: SSH username
            user_data: Cloud-init user data string
            network_data: Cloud-init network data string (optional)
            guest_cluster_id: Management cluster ID (c-m-xxxxx) for LB
                labels. When set, adds harvesterhci.io/creator and
                guestcluster.harvesterhci.io/name labels so the
                Harvester LB webhook can discover the VMs.
        """
        pass

    @abstractmethod
    def install_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Install a Helm chart on a guest cluster via Rancher catalog API."""
        pass

    @abstractmethod
    def upgrade_chart(self, cluster_id, repo_name, chart_name, version,
                      release_name, namespace, values=None):
        """Upgrade an installed Helm chart on a guest cluster."""
        pass

    @abstractmethod
    def uninstall_chart(self, cluster_id, release_name, namespace):
        """Uninstall a Helm chart (app) from a guest cluster."""
        pass

    @abstractmethod
    def wait_for_chart_app_deleted(self, cluster_id, release_name,
                                   namespace, timeout):
        """Wait for a chart app to be fully removed from a guest cluster."""
        pass

    @abstractmethod
    def create_cluster_repo(self, cluster_id, repo_name, git_url, git_branch):
        """Create a ClusterRepo on a guest cluster."""
        pass

    @abstractmethod
    def wait_for_cluster_repo_ready(self, cluster_id, repo_name, timeout,
                                    expected_git_branch=None):
        """Wait for a ClusterRepo to finish downloading on a guest cluster."""
        pass

    @abstractmethod
    def get_chart_versions(self, repo_name, chart_name, cluster_id=None):
        """Get available versions for a chart from a Rancher chart repo."""
        pass

    @abstractmethod
    def get_deployed_chart_version(self, cluster_id, release_name, namespace):
        """Return the deployed version string of an installed chart app."""
        pass

    @abstractmethod
    def create_cloud_config_secret(self, cluster_id, secret_name,
                                   namespace, kubeconfig):
        """Create a cloud-provider-config secret on a guest cluster."""
        pass

    @abstractmethod
    def write_cloud_config_to_nodes(self, cluster_id, secret_name, namespace):
        """Write cloud-provider-config to each node hostPath via a DaemonSet."""
        pass

    @abstractmethod
    def wait_for_chart_app_ready(self, cluster_id, release_name,
                                 namespace, timeout, expected_version=None):
        """Wait for a chart app to be deployed and ready."""
        pass

    # RWX Volume / StorageClass / StatefulSet Operations
    @abstractmethod
    def create_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Create an RWX-capable StorageClass on the host Harvester cluster.

        Uses Longhorn provisioner with NFS options for ReadWriteMany support.

        Args:
            name: StorageClass name (default: longhorn-rwx)
        """
        pass

    @abstractmethod
    def delete_rwx_storage_class_on_host(self, name="longhorn-rwx"):
        """Delete the RWX StorageClass from the host Harvester cluster.

        Args:
            name: StorageClass name (default: longhorn-rwx)
        """
        pass

    @abstractmethod
    def create_guest_storage_class(self, cluster_id, name, host_storage_class="longhorn-rwx"):
        """Create a StorageClass on the guest cluster referencing a host StorageClass.

        Uses the Harvester CSI driver provisioner with hostStorageClass parameter.

        Args:
            cluster_id: Guest cluster ID
            name: StorageClass name on the guest cluster
            host_storage_class: Name of the host-side StorageClass to reference
        """
        pass

    @abstractmethod
    def delete_guest_storage_class(self, cluster_id, name):
        """Delete a StorageClass from the guest cluster.

        Args:
            cluster_id: Guest cluster ID
            name: StorageClass name
        """
        pass

    @abstractmethod
    def create_pvc_rwx(self, cluster_id, name, size="1Gi", storage_class=None):
        """Create a ReadWriteMany PVC in guest cluster.

        Args:
            cluster_id: Cluster ID
            name: PVC name
            size: Storage size (default: 1Gi)
            storage_class: Storage class name (optional)
        """
        pass

    @abstractmethod
    def create_statefulset(self, cluster_id, namespace, name, image,
                           pvc_name, replicas=2):
        """Create a StatefulSet that mounts an existing PVC.

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            name: StatefulSet name
            image: Container image
            pvc_name: Existing PVC name to mount
            replicas: Number of replicas (default: 2)
        """
        pass

    @abstractmethod
    def get_statefulset(self, cluster_id, namespace, name):
        """Get StatefulSet details."""
        pass

    @abstractmethod
    def delete_statefulset(self, cluster_id, namespace, name):
        """Delete StatefulSet."""
        pass

    @abstractmethod
    def wait_for_statefulset_ready(self, cluster_id, namespace, name,
                                   timeout):
        """Wait for StatefulSet to have all replicas ready."""
        pass

    @abstractmethod
    def exec_command_in_pod(self, cluster_id, namespace, pod_name, command):
        """Execute a command inside a pod.

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            pod_name: Pod name
            command: Command list to execute

        Returns:
            str: Command stdout
        """
        pass

    @abstractmethod
    def get_pods_by_label(self, cluster_id, namespace, label_selector):
        """Get pods matching a label selector.

        Args:
            cluster_id: Cluster ID
            namespace: Namespace
            label_selector: Label selector string (e.g. 'name=myapp')

        Returns:
            list: List of pod dicts
        """
        pass

    # Rancher RBAC Operations

    @abstractmethod
    def create_rancher_user(self, user_id, display_name):
        """Create a new Rancher local user.

        Args:
            user_id: Username (used as both metadata.name and username field)
            display_name: Human-readable display name

        Returns:
            dict: Created user resource
        """
        pass

    @abstractmethod
    def delete_rancher_user(self, user_id):
        """Delete a Rancher user and its associated password secret.

        Args:
            user_id: Username to delete
        """
        pass

    @abstractmethod
    def set_user_password(self, user_id, password):
        """Create or update the password Secret for a Rancher local user.

        The secret is stored in the cattle-local-user-passwords namespace.

        Args:
            user_id: Username whose password to set
            password: Plaintext password value
        """
        pass

    @abstractmethod
    def assign_standard_user_role(self, user_id):
        """Assign the Standard User global role (globalRoleName: user) via GlobalRoleBinding.

        Args:
            user_id: Username to assign the role to
        """
        pass

    @abstractmethod
    def create_project(self, cluster_id, display_name):
        """Create a new Rancher project in the given cluster.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            display_name: Display name for the project

        Returns:
            str: Short project ID (e.g. p-xxxxx)
        """
        pass

    @abstractmethod
    def delete_project(self, cluster_id, project_id):
        """Delete a Rancher project.

        Args:
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        pass

    @abstractmethod
    def assign_project_role(self, user_id, cluster_id, project_id, role_template_name):
        """Create a ProjectRoleTemplateBinding to grant a user a project-scoped role.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-project-view)
        """
        pass

    @abstractmethod
    def delete_user_global_role_bindings(self, user_id):
        """Delete all GlobalRoleBindings owned by the given user.

        Args:
            user_id: Username whose GlobalRoleBindings to remove
        """
        pass

    @abstractmethod
    def delete_user_project_role_bindings(self, user_id, cluster_id, project_id):
        """Delete all ProjectRoleTemplateBindings owned by the user in a project.

        Args:
            user_id: Username whose bindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        pass

    @abstractmethod
    def assign_cluster_role(self, user_id, cluster_id, role_template_name):
        """Create a ClusterRoleTemplateBinding to grant a user a cluster-scoped role.

        Args:
            user_id: Username to assign the role to
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            role_template_name: RoleTemplate name (e.g. virt-cluster-view)
        """
        pass

    @abstractmethod
    def delete_user_cluster_role_bindings(self, user_id, cluster_id):
        """Delete all ClusterRoleTemplateBindings owned by the given user in a cluster.

        Args:
            user_id: Username whose ClusterRoleTemplateBindings to remove
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        pass

    @abstractmethod
    def generate_user_kubeconfig(self, user_id, password, cluster_id):
        """Login as user_id and return a kubeconfig YAML string for cluster_id.

        Authenticates against Rancher's local auth provider, then calls
        generateKubeconfig to produce a ready-to-use kubeconfig for the cluster.

        Args:
            user_id: Rancher username
            password: User's login password
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)

        Returns:
            str: kubeconfig YAML content
        """
        pass

    @abstractmethod
    def verify_resource_access(self, kubeconfig_content, verb, resource, namespace):
        """Run 'kubectl auth can-i <verb> <resource> -n <namespace>' with the given kubeconfig.

        Args:
            kubeconfig_content: kubeconfig YAML string (from generate_user_kubeconfig)
            verb: kubectl verb (e.g. 'get', 'create')
            resource: Resource type (e.g. 'virtualmachines.kubevirt.io')
            namespace: Target namespace

        Returns:
            tuple: (True, output) if allowed, (False, output) if denied
        """
        pass

    @abstractmethod
    def create_namespace_in_project(self, namespace_name, cluster_id, project_id):
        """Create a namespace in the Harvester cluster and bind it to a Rancher project.

        Args:
            namespace_name: Name for the new namespace
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
            project_id: Short project ID (e.g. p-xxxxx)
        """
        pass

    @abstractmethod
    def delete_namespace_from_cluster(self, namespace_name, cluster_id):
        """Delete a namespace from the Harvester cluster (best-effort).

        Args:
            namespace_name: Namespace to delete
            cluster_id: Management cluster ID (e.g. c-m-xxxxx)
        """
        pass
