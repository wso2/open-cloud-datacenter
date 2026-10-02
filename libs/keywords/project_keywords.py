"""
Project Keywords - creates Project() instance and delegates - NO direct API calls!
Layer 3: Keyword wrappers for Robot Framework
"""
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa E402
from utility.utility import logging  # noqa E402
from project import Project  # noqa E402
from constant import DEFAULT_TIMEOUT  # noqa E402


class project_keywords:
    """Project keyword wrapper - creates Project component and delegates"""

    def __init__(self):
        """Lazy init so the API clients can be set up before first use"""
        self._project = None

    @property
    def project(self):
        if self._project is None:
            self._project = Project()
        return self._project

    def cleanup_projects(self):
        """Clean up all test projects"""
        self.project.cleanup()

    def create_project(self, display_name, cpu_limit=None, memory_limit=None,
                       ns_default_cpu=None, ns_default_memory=None,
                       description=None):
        """
        Create a project, optionally with CPU/memory resource limits.

        Returns:
            str: Generated project id (e.g. p-xxxxx)
        """
        logging(f'Creating project {display_name}')
        return self.project.create(
            display_name, cpu_limit, memory_limit,
            ns_default_cpu, ns_default_memory, description
        )

    def project_exists(self, display_name):
        """Check whether a project exists"""
        return self.project.exists(display_name)

    def get_project_id(self, display_name):
        """Get the generated project id for a display name"""
        return self.project.get_id(display_name)

    def list_projects(self):
        """List projects in the local cluster"""
        return self.project.list()

    def wait_for_project_active(self, display_name, timeout=DEFAULT_TIMEOUT):
        """Wait for the project to become active"""
        logging(f'Waiting for project {display_name} to be active')
        return self.project.wait_for_active(display_name, int(timeout))

    def get_project_resource_limits(self, display_name):
        """
        Get the resource limits configured on a project.

        Returns:
            tuple: (cpu_limit, memory_limit) as strings
        """
        cpu, memory = self.project.get_resource_limits(display_name)
        logging(f'Project {display_name} limits: cpu={cpu}, memory={memory}')
        return cpu, memory

    def delete_project(self, display_name):
        """Delete a project"""
        logging(f'Deleting project {display_name}')
        self.project.delete(display_name)

    def wait_for_project_deleted(self, display_name, timeout=DEFAULT_TIMEOUT):
        """Wait for the project to be fully removed"""
        logging(f'Waiting for project {display_name} to be deleted')
        return self.project.wait_for_deleted(display_name, int(timeout))

    # Negative-test helpers (return {success, code, message})
    def try_create_project(self, display_name, cpu_limit=None,
                           memory_limit=None, ns_default_cpu=None,
                           ns_default_memory=None):
        """Attempt to create a project expected to be rejected"""
        return self.project.try_create(
            display_name, cpu_limit, memory_limit,
            ns_default_cpu, ns_default_memory
        )

    def try_delete_project(self, display_name):
        """Attempt to delete a project expected to be missing/rejected"""
        return self.project.try_delete(display_name)
