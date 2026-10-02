"""
Namespace Component - delegates to CRD or REST implementation

The implementation is selected based on the HARVESTER_OPERATION_STRATEGY
environment variable. Valid values are 'crd' or 'rest'. Defaults to 'crd'.
"""
import os

from constant import HarvesterOperationStrategy, DEFAULT_TIMEOUT
from namespace.rest import Rest
from namespace.crd import CRD
from namespace.base import Base


class Namespace(Base):
    """Namespace component - selects implementation by HARVESTER_OPERATION_STRATEGY"""

    def __init__(self):
        strategy_str = os.getenv("HARVESTER_OPERATION_STRATEGY", "crd").lower()
        try:
            self._strategy = HarvesterOperationStrategy(strategy_str)
        except ValueError:
            self._strategy = HarvesterOperationStrategy.CRD

        if self._strategy == HarvesterOperationStrategy.REST:
            self.namespace = Rest()
        else:
            self.namespace = CRD()

    def create(self, name, project_name=None, cpu_limit=None,
               memory_limit=None):
        return self.namespace.create(name, project_name, cpu_limit,
                                     memory_limit)

    def get(self, name):
        return self.namespace.get(name)

    def exists(self, name):
        return self.namespace.exists(name)

    def list(self, label_selector=None):
        return self.namespace.list(label_selector)

    def wait_for_active(self, name, timeout=DEFAULT_TIMEOUT):
        return self.namespace.wait_for_active(name, timeout)

    def get_resource_limits(self, name):
        return self.namespace.get_resource_limits(name)

    def get_project(self, name):
        return self.namespace.get_project(name)

    def delete(self, name):
        return self.namespace.delete(name)

    def wait_for_deleted(self, name, timeout=DEFAULT_TIMEOUT):
        return self.namespace.wait_for_deleted(name, timeout)

    def try_create(self, name, project_name=None, cpu_limit=None,
                   memory_limit=None):
        return self.namespace.try_create(name, project_name, cpu_limit,
                                         memory_limit)

    def try_delete(self, name):
        return self.namespace.try_delete(name)

    def cleanup(self):
        return self.namespace.cleanup()
