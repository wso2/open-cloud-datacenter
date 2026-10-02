"""
Namespace Rest Implementation - Harvester REST API operations
Uses api.namespaces (apiclient/harvester_api NamespaceManager) obtained
through get_harvester_api_client().
"""
import time
from datetime import datetime, timedelta

from utility.utility import (
    logging, get_harvester_api_client, get_retry_count_and_interval
)
from constant import LABEL_TEST, LABEL_TEST_VALUE, DEFAULT_TIMEOUT
from namespace.base import Base


class Rest(Base):
    """Namespace Rest implementation - makes actual API calls"""

    def __init__(self):
        self.retry_count, self.retry_interval = get_retry_count_and_interval()

    @property
    def api(self):
        return get_harvester_api_client()

    @staticmethod
    def _message(data):
        if isinstance(data, dict):
            return data.get("message") or str(data)
        return str(data)

    def _build_metadata(self, project_name, cpu_limit, memory_limit):
        project_id = None
        if project_name:
            from project import Project
            project_id = Project().get_id(project_name)
        return self.build_metadata(project_id, cpu_limit, memory_limit)

    def create(self, name, project_name=None, cpu_limit=None,
               memory_limit=None):
        labels, annotations = self._build_metadata(
            project_name, cpu_limit, memory_limit
        )
        logging(f"Creating namespace '{name}' via REST "
                f"(project={project_name}, cpu={cpu_limit}, "
                f"memory={memory_limit})")
        code, data = self.api.namespaces.create(
            name, labels=labels, annotations=annotations
        )
        assert code in (200, 201), \
            f"Failed to create namespace '{name}': {code}, {data}"
        logging(f"Created namespace '{name}'")
        return name

    def try_create(self, name, project_name=None, cpu_limit=None,
                   memory_limit=None):
        labels, annotations = self._build_metadata(
            project_name, cpu_limit, memory_limit
        )
        code, data = self.api.namespaces.create(
            name, labels=labels, annotations=annotations
        )
        return {"success": code in (200, 201), "code": code,
                "message": "" if code in (200, 201)
                else self._message(data)}

    def get(self, name):
        code, data = self.api.namespaces.get(name)
        if code == 404:
            return None
        assert code == 200, f"Failed to get namespace '{name}': {code}, {data}"
        return data

    def exists(self, name):
        try:
            return self.get(name) is not None
        except Exception as e:
            logging(f"Error checking namespace '{name}': {e}", 'WARNING')
            return False

    def list(self, label_selector=None):
        code, data = self.api.namespaces.get()
        assert code == 200, f"Failed to list namespaces: {code}, {data}"
        items = data.get("data", [])

        if label_selector:
            key, _, value = label_selector.partition("=")
            items = [i for i in items
                     if i.get("metadata", {}).get("labels", {}).get(key)
                     == value]
        return items

    def wait_for_active(self, name, timeout=DEFAULT_TIMEOUT):
        logging(f"Waiting for namespace '{name}' to be Active")
        endtime = datetime.now() + timedelta(seconds=int(timeout))
        namespace = None
        while endtime > datetime.now():
            try:
                namespace = self.get(name)
                if namespace and self.is_active(namespace):
                    logging(f"Namespace '{name}' is Active")
                    return namespace
            except Exception as e:
                logging(f"Error polling namespace '{name}': {e}", 'WARNING')
            time.sleep(self.retry_interval)

        phase = (namespace or {}).get("status", {}).get("phase")
        raise AssertionError(
            f"Namespace '{name}' not Active within {timeout}s "
            f"(exists={namespace is not None}, phase={phase})"
        )

    def get_resource_limits(self, name):
        namespace = self.get(name)
        if namespace is None:
            raise AssertionError(f"Namespace '{name}' not found")
        return self.extract_limits(namespace)

    def get_project(self, name):
        namespace = self.get(name)
        if namespace is None:
            raise AssertionError(f"Namespace '{name}' not found")
        return self.get_project_id(namespace)

    def delete(self, name):
        logging(f"Deleting namespace '{name}'")
        code, data = self.api.namespaces.delete(name)
        assert code in (200, 204, 404), \
            f"Failed to delete namespace '{name}': {code}, {data}"

    def try_delete(self, name):
        code, data = self.api.namespaces.delete(name)
        return {"success": code in (200, 204), "code": code,
                "message": "" if code in (200, 204)
                else self._message(data)}

    def wait_for_deleted(self, name, timeout=DEFAULT_TIMEOUT):
        logging(f"Waiting for namespace '{name}' to be deleted")
        endtime = datetime.now() + timedelta(seconds=int(timeout))
        while endtime > datetime.now():
            if self.get(name) is None:
                logging(f"Namespace '{name}' deleted")
                return True
            time.sleep(self.retry_interval)
        raise AssertionError(
            f"Namespace '{name}' still exists after {timeout}s"
        )

    def cleanup(self):
        logging('Cleaning up test namespaces')
        try:
            namespaces = self.list(
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )
            for namespace in namespaces:
                name = namespace["metadata"]["name"]
                try:
                    logging(f"Deleting test namespace: {name}")
                    self.delete(name)
                except Exception as e:
                    logging(f"Error deleting namespace {name}: {e}",
                            'WARNING')
        except Exception as e:
            logging(f"Error during namespace cleanup: {e}", 'WARNING')
