"""
Namespace Keywords - creates Namespace() instance and delegates - NO direct API calls!
Layer 3: Keyword wrappers for Robot Framework
"""
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa E402
from utility.utility import logging  # noqa E402
from namespace import Namespace  # noqa E402
from constant import DEFAULT_TIMEOUT  # noqa E402


class namespace_keywords:
    """Namespace keyword wrapper - creates Namespace component and delegates"""

    def __init__(self):
        """Lazy init so the API clients can be set up before first use"""
        self._namespace = None

    @property
    def namespace(self):
        if self._namespace is None:
            self._namespace = Namespace()
        return self._namespace

    def cleanup_namespaces(self):
        """Clean up all test namespaces"""
        self.namespace.cleanup()

    def create_namespace(self, name, project_name=None, cpu_limit=None,
                         memory_limit=None):
        """
        Create a namespace, optionally inside a project and with limits.

        Returns:
            str: Namespace name
        """
        logging(f'Creating namespace {name}')
        return self.namespace.create(name, project_name, cpu_limit,
                                     memory_limit)

    def namespace_exists(self, name):
        """Check whether a namespace exists"""
        return self.namespace.exists(name)

    def list_namespaces(self):
        """List namespaces"""
        return self.namespace.list()

    def wait_for_namespace_active(self, name, timeout=DEFAULT_TIMEOUT):
        """Wait for the namespace to become Active"""
        logging(f'Waiting for namespace {name} to be Active')
        return self.namespace.wait_for_active(name, int(timeout))

    def get_namespace_resource_limits(self, name):
        """
        Get the resource limits configured on a namespace.

        Returns:
            tuple: (cpu_limit, memory_limit) as strings
        """
        cpu, memory = self.namespace.get_resource_limits(name)
        logging(f'Namespace {name} limits: cpu={cpu}, memory={memory}')
        return cpu, memory

    def get_namespace_project(self, name):
        """Get the short project id (p-xxxxx) the namespace is bound to"""
        return self.namespace.get_project(name)

    def delete_namespace(self, name):
        """Delete a namespace"""
        logging(f'Deleting namespace {name}')
        self.namespace.delete(name)

    def wait_for_namespace_deleted(self, name, timeout=DEFAULT_TIMEOUT):
        """Wait for the namespace to be fully removed"""
        logging(f'Waiting for namespace {name} to be deleted')
        return self.namespace.wait_for_deleted(name, int(timeout))

    # Negative-test helpers (return {success, code, message})
    def try_create_namespace(self, name, project_name=None, cpu_limit=None,
                             memory_limit=None):
        """Attempt to create a namespace expected to be rejected"""
        return self.namespace.try_create(name, project_name, cpu_limit,
                                         memory_limit)

    def try_delete_namespace(self, name):
        """Attempt to delete a namespace expected to be missing/rejected"""
        return self.namespace.try_delete(name)
