"""
Namespace CRD Implementation - Kubernetes API operations
Uses the core v1 Namespace API; project binding and quota are expressed as
Rancher labels/annotations on the namespace.
"""
import time
from datetime import datetime, timedelta

from kubernetes import client
from kubernetes.client.rest import ApiException
from constant import LABEL_TEST, LABEL_TEST_VALUE, DEFAULT_TIMEOUT
from namespace.base import Base
from utility.utility import logging, get_retry_count_and_interval


class CRD(Base):
    """Namespace CRD implementation"""

    def __init__(self):
        self.core_api = client.CoreV1Api()
        self.retry_count, self.retry_interval = get_retry_count_and_interval()

    def _to_dict(self, obj):
        """Serialize a client model to a camelCase dict (like the raw API)"""
        return client.ApiClient().sanitize_for_serialization(obj)

    def _build_body(self, name, project_name, cpu_limit, memory_limit):
        project_id = None
        if project_name:
            from project import Project
            project_id = Project().get_id(project_name)

        labels, annotations = self.build_metadata(
            project_id, cpu_limit, memory_limit
        )
        return client.V1Namespace(
            metadata=client.V1ObjectMeta(
                name=name, labels=labels, annotations=annotations
            )
        )

    def create(self, name, project_name=None, cpu_limit=None,
               memory_limit=None):
        body = self._build_body(name, project_name, cpu_limit, memory_limit)
        logging(f"Creating namespace '{name}' "
                f"(project={project_name}, cpu={cpu_limit}, "
                f"memory={memory_limit})")
        try:
            self.core_api.create_namespace(body=body)
        except ApiException as e:
            raise Exception(
                f"Failed to create namespace '{name}': "
                f"{e.status}, {e.reason}, {e.body}"
            )
        logging(f"Created namespace '{name}'")
        return name

    def try_create(self, name, project_name=None, cpu_limit=None,
                   memory_limit=None):
        try:
            body = self._build_body(name, project_name, cpu_limit,
                                    memory_limit)
            self.core_api.create_namespace(body=body)
            return {"success": True, "code": 201, "message": ""}
        except ApiException as e:
            logging(f"Create of namespace '{name}' rejected: "
                    f"status={e.status}: {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def get(self, name):
        try:
            return self._to_dict(self.core_api.read_namespace(name))
        except ApiException as e:
            if e.status == 404:
                return None
            raise

    def exists(self, name):
        try:
            return self.get(name) is not None
        except Exception as e:
            logging(f"Error checking namespace '{name}': {e}", 'WARNING')
            return False

    def list(self, label_selector=None):
        result = self.core_api.list_namespace(label_selector=label_selector)
        return [self._to_dict(item) for item in result.items]

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
        try:
            self.core_api.delete_namespace(name)
        except ApiException as e:
            if e.status != 404:
                raise Exception(
                    f"Failed to delete namespace '{name}': "
                    f"{e.status}, {e.reason}, {e.body}"
                )

    def try_delete(self, name):
        try:
            self.core_api.delete_namespace(name)
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

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
