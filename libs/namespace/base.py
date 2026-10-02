"""
Base class for Namespace operations
Defines the interface for both CRD and REST implementations, plus the pure
helpers (labels / annotations building and parsing) shared by both.

A namespace is bound to a Rancher project through the
`field.cattle.io/projectId` label + annotation, and carries its own quota in
the `field.cattle.io/resourceQuota` annotation.
"""
import json
from abc import ABC, abstractmethod

from constant import (
    EXISTING_HARVESTER_NAME, LABEL_TEST, LABEL_TEST_VALUE,
    ANNOT_PROJECT_ID, LABEL_PROJECT_ID, ANNOT_RESOURCE_QUOTA,
    QUOTA_LIMITS_CPU, QUOTA_LIMITS_MEMORY, NAMESPACE_PHASE_ACTIVE,
)


class Base(ABC):
    """Base class for Namespace implementations"""

    # ------------------------------------------------------------------
    # Shared helpers (no I/O)
    # ------------------------------------------------------------------
    @staticmethod
    def build_metadata(project_id=None, cpu_limit=None, memory_limit=None):
        """Build (labels, annotations) for a test namespace.

        Args:
            project_id: Short project id (p-xxxxx) to bind to, optional
            cpu_limit: Namespace CPU limit (e.g. "4"), optional
            memory_limit: Namespace memory limit (e.g. "8Gi"), optional
        """
        labels = {LABEL_TEST: LABEL_TEST_VALUE}
        annotations = {}

        if project_id:
            labels[LABEL_PROJECT_ID] = project_id
            annotations[ANNOT_PROJECT_ID] = f"{EXISTING_HARVESTER_NAME}:{project_id}"

        limit = {}
        if cpu_limit not in (None, ""):
            limit[QUOTA_LIMITS_CPU] = str(cpu_limit)
        if memory_limit not in (None, ""):
            limit[QUOTA_LIMITS_MEMORY] = str(memory_limit)
        if limit:
            annotations[ANNOT_RESOURCE_QUOTA] = json.dumps({"limit": limit})

        return labels, annotations

    @staticmethod
    def extract_limits(namespace):
        """Return (cpu_limit, memory_limit) strings from a namespace object.

        Missing limits are returned as empty strings.
        """
        raw = (namespace.get("metadata", {})
               .get("annotations", {}) or {}).get(ANNOT_RESOURCE_QUOTA)
        if not raw:
            return "", ""
        limit = json.loads(raw).get("limit", {}) or {}
        return (str(limit.get(QUOTA_LIMITS_CPU, "")),
                str(limit.get(QUOTA_LIMITS_MEMORY, "")))

    @staticmethod
    def get_project_id(namespace):
        """Return the short project id the namespace is bound to, or ''."""
        metadata = namespace.get("metadata", {})
        return (metadata.get("labels", {}) or {}).get(LABEL_PROJECT_ID, "")

    @staticmethod
    def is_active(namespace):
        """True when the namespace phase is Active and it is not deleting."""
        if namespace.get("metadata", {}).get("deletionTimestamp"):
            return False
        return (namespace.get("status", {}).get("phase")
                == NAMESPACE_PHASE_ACTIVE)

    # ------------------------------------------------------------------
    # Interface
    # ------------------------------------------------------------------
    @abstractmethod
    def create(self, name, project_name=None, cpu_limit=None,
               memory_limit=None):
        """Create a namespace, optionally inside a project.

        Args:
            name: Namespace name
            project_name: Project display name to bind to, optional
            cpu_limit: Namespace CPU limit (e.g. "4"), optional
            memory_limit: Namespace memory limit (e.g. "8Gi"), optional

        Returns:
            str: Namespace name
        """
        pass

    @abstractmethod
    def get(self, name):
        """Get a namespace, returns None if not found"""
        pass

    @abstractmethod
    def exists(self, name):
        """Check whether a namespace exists"""
        pass

    @abstractmethod
    def list(self, label_selector=None):
        """List namespaces"""
        pass

    @abstractmethod
    def wait_for_active(self, name, timeout):
        """Wait for the namespace to become Active"""
        pass

    @abstractmethod
    def get_resource_limits(self, name):
        """Return (cpu_limit, memory_limit) configured on the namespace"""
        pass

    @abstractmethod
    def get_project(self, name):
        """Return the short project id the namespace is bound to ('' if none)"""
        pass

    @abstractmethod
    def delete(self, name):
        """Delete a namespace (no-op if it does not exist)"""
        pass

    @abstractmethod
    def wait_for_deleted(self, name, timeout):
        """Wait for the namespace to be fully removed"""
        pass

    @abstractmethod
    def try_create(self, name, project_name=None, cpu_limit=None,
                   memory_limit=None):
        """Attempt a create for negative testing; returns
        {success, code, message} and never raises on API rejection."""
        pass

    @abstractmethod
    def try_delete(self, name):
        """Attempt a delete for negative testing; returns
        {success, code, message}. A missing namespace is reported as 404."""
        pass

    @abstractmethod
    def cleanup(self):
        """Delete all test namespaces (labelled with LABEL_TEST)"""
        pass
