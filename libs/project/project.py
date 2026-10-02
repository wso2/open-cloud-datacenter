"""
Project Component - delegates to the Rancher REST implementation.
"""
from constant import DEFAULT_TIMEOUT
from project.rest import Rest
from project.base import Base


class Project(Base):
    """Project component backed by Rancher's v3 Project API."""

    def __init__(self):
        self.project = Rest()

    def create(self, display_name, cpu_limit=None, memory_limit=None,
               ns_default_cpu=None, ns_default_memory=None,
               description=None):
        return self.project.create(
            display_name, cpu_limit, memory_limit,
            ns_default_cpu, ns_default_memory, description
        )

    def get(self, display_name):
        return self.project.get(display_name)

    def get_id(self, display_name):
        return self.project.get_id(display_name)

    def exists(self, display_name):
        return self.project.exists(display_name)

    def list(self, label_selector=None):
        return self.project.list(label_selector)

    def wait_for_active(self, display_name, timeout=DEFAULT_TIMEOUT):
        return self.project.wait_for_active(display_name, timeout)

    def get_resource_limits(self, display_name):
        return self.project.get_resource_limits(display_name)

    def delete(self, display_name):
        return self.project.delete(display_name)

    def wait_for_deleted(self, display_name, timeout=DEFAULT_TIMEOUT):
        return self.project.wait_for_deleted(display_name, timeout)

    def try_create(self, display_name, cpu_limit=None, memory_limit=None,
                   ns_default_cpu=None, ns_default_memory=None):
        return self.project.try_create(
            display_name, cpu_limit, memory_limit,
            ns_default_cpu, ns_default_memory
        )

    def try_delete(self, display_name):
        return self.project.try_delete(display_name)

    def cleanup(self):
        return self.project.cleanup()
