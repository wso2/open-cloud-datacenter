"""
Base class for Project operations
Defines the interface for both CRD and REST implementations, plus the pure
helpers (manifest building / parsing) shared by both.

A Harvester "project" is a Rancher management.cattle.io/v3 Project living in
the `local` cluster namespace. Its metadata.name is generated (p-xxxxx); tests
address it by spec.displayName, so every operation here takes the display name.
"""
from abc import ABC, abstractmethod

from constant import (
    EXISTING_HARVESTER_NAME, LABEL_TEST, LABEL_TEST_VALUE,
    LOCAL_CLUSTER_ID, QUOTA_LIMITS_CPU, QUOTA_LIMITS_MEMORY,
    PROJECT_ID_PREFIX,
)


class Base(ABC):
    """Base class for Project implementations"""

    # ------------------------------------------------------------------
    # Shared helpers (no I/O)
    # ------------------------------------------------------------------
    @staticmethod
    def build_quota_limit(cpu_limit=None, memory_limit=None):
        """Build a Rancher quota `limit` dict from CPU/memory values.

        Empty / None values are omitted so an unset limit stays unlimited.
        """
        limit = {}
        if cpu_limit not in (None, ""):
            limit[QUOTA_LIMITS_CPU] = str(cpu_limit)
        if memory_limit not in (None, ""):
            limit[QUOTA_LIMITS_MEMORY] = str(memory_limit)
        return limit

    @classmethod
    def build_manifest(cls, display_name, cpu_limit=None, memory_limit=None,
                       ns_default_cpu=None, ns_default_memory=None,
                       description=None):
        """Build the management.cattle.io/v3 Project manifest.

        Rancher requires the namespace default quota to cover the same keys
        as the project quota (and to be <= it), so when no explicit namespace
        default is given it defaults to the project limit.
        """
        project_limit = cls.build_quota_limit(cpu_limit, memory_limit)
        ns_default_limit = cls.build_quota_limit(
            ns_default_cpu if ns_default_cpu not in (None, "") else cpu_limit,
            ns_default_memory if ns_default_memory not in (None, "")
            else memory_limit,
        )

        spec = {
            "clusterName": EXISTING_HARVESTER_NAME,
            "displayName": display_name,
            "containerDefaultResourceLimit": {},
            "namespaceDefaultResourceQuota": {"limit": ns_default_limit},
            "resourceQuota": {"limit": project_limit},
        }
        if description:
            spec["description"] = description

        return {
            "apiVersion": "management.cattle.io/v3",
            "kind": "Project",
            "metadata": {
                "generateName": PROJECT_ID_PREFIX,
                "namespace": LOCAL_CLUSTER_ID,
                "labels": {LABEL_TEST: LABEL_TEST_VALUE},
            },
            "spec": spec,
        }

    @staticmethod
    def find_by_display_name(items, display_name):
        """Return the project whose spec.displayName matches, else None."""
        for item in items or []:
            if item.get("spec", {}).get("displayName") == display_name:
                return item
        return None

    @staticmethod
    def extract_limits(project):
        """Return (cpu_limit, memory_limit) strings from a project object.

        Missing limits are returned as empty strings.
        """
        limit = (project.get("spec", {})
                 .get("resourceQuota", {})
                 .get("limit", {})) or {}
        return (str(limit.get(QUOTA_LIMITS_CPU, "")),
                str(limit.get(QUOTA_LIMITS_MEMORY, "")))

    @staticmethod
    def is_active(project):
        """True when the project is fully provisioned.

        Steve (REST) objects carry metadata.state; raw CRs only carry status
        conditions, so fall back to the conditions the Rancher controllers
        set once the backing namespace and initial roles exist.
        """
        metadata = project.get("metadata", {})
        if metadata.get("deletionTimestamp"):
            return False

        state = metadata.get("state")
        if state:
            return (str(state.get("name", "")).lower() == "active"
                    and not state.get("error"))

        conditions = project.get("status", {}).get("conditions", []) or []
        true_types = {c.get("type") for c in conditions
                      if str(c.get("status")) == "True"}
        return {"BackingNamespaceCreated",
                "InitialRolesPopulated"} <= true_types

    # ------------------------------------------------------------------
    # Interface
    # ------------------------------------------------------------------
    @abstractmethod
    def create(self, display_name, cpu_limit=None, memory_limit=None,
               ns_default_cpu=None, ns_default_memory=None,
               description=None):
        """Create a project.

        Args:
            display_name: Project display name
            cpu_limit: Project CPU limit (e.g. "4"), optional
            memory_limit: Project memory limit (e.g. "8Gi"), optional
            ns_default_cpu: Default per-namespace CPU limit, optional
            ns_default_memory: Default per-namespace memory limit, optional
            description: Project description, optional

        Returns:
            str: Generated project id (e.g. p-xxxxx)
        """
        pass

    @abstractmethod
    def get(self, display_name):
        """Get a project by display name.

        Returns:
            dict or None: Project data, or None if not found
        """
        pass

    @abstractmethod
    def get_id(self, display_name):
        """Get the generated project id (p-xxxxx) for a display name."""
        pass

    @abstractmethod
    def exists(self, display_name):
        """Check whether a project exists"""
        pass

    @abstractmethod
    def list(self, label_selector=None):
        """List projects in the local cluster"""
        pass

    @abstractmethod
    def wait_for_active(self, display_name, timeout):
        """Wait for the project to become active"""
        pass

    @abstractmethod
    def get_resource_limits(self, display_name):
        """Return (cpu_limit, memory_limit) configured on the project"""
        pass

    @abstractmethod
    def delete(self, display_name):
        """Delete a project (no-op if it does not exist)"""
        pass

    @abstractmethod
    def wait_for_deleted(self, display_name, timeout):
        """Wait for the project to be fully removed"""
        pass

    @abstractmethod
    def try_create(self, display_name, cpu_limit=None, memory_limit=None,
                   ns_default_cpu=None, ns_default_memory=None):
        """Attempt a create for negative testing; returns
        {success, code, message} and never raises on API rejection."""
        pass

    @abstractmethod
    def try_delete(self, display_name):
        """Attempt a delete for negative testing; returns
        {success, code, message}. A missing project is reported as 404."""
        pass

    @abstractmethod
    def cleanup(self):
        """Delete all test projects (labelled with LABEL_TEST)"""
        pass
