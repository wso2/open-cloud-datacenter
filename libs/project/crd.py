"""
Project CRD Implementation
Uses the Rancher management.cattle.io/v3 Project custom resource, which lives
in the `local` cluster namespace of the Harvester cluster.
"""
import time
from datetime import datetime, timedelta

from kubernetes.client.rest import ApiException
from crd import create_cr, delete_cr, list_cr
from constant import (
    RANCHER_MGMT_GROUP, RANCHER_MGMT_VERSION, PROJECT_PLURAL,
    LOCAL_CLUSTER_ID, LABEL_TEST, LABEL_TEST_VALUE, DEFAULT_TIMEOUT,
)
from project.base import Base
from utility.utility import logging, get_retry_count_and_interval


class CRD(Base):
    """Project CRD implementation"""

    def __init__(self):
        self.retry_count, self.retry_interval = get_retry_count_and_interval()

    def create(self, display_name, cpu_limit=None, memory_limit=None,
               ns_default_cpu=None, ns_default_memory=None,
               description=None):
        body = self.build_manifest(
            display_name, cpu_limit, memory_limit,
            ns_default_cpu, ns_default_memory, description
        )
        logging(f"Creating project '{display_name}' "
                f"(cpu={cpu_limit}, memory={memory_limit})")
        try:
            obj = create_cr(
                group=RANCHER_MGMT_GROUP,
                version=RANCHER_MGMT_VERSION,
                namespace=LOCAL_CLUSTER_ID,
                plural=PROJECT_PLURAL,
                body=body
            )
        except ApiException as e:
            raise Exception(
                f"Failed to create project '{display_name}': "
                f"{e.status}, {e.reason}, {e.body}"
            )

        project_id = obj["metadata"]["name"]
        logging(f"Created project '{display_name}': {project_id}")
        return project_id

    def try_create(self, display_name, cpu_limit=None, memory_limit=None,
                   ns_default_cpu=None, ns_default_memory=None):
        body = self.build_manifest(
            display_name, cpu_limit, memory_limit,
            ns_default_cpu, ns_default_memory
        )
        try:
            create_cr(
                group=RANCHER_MGMT_GROUP,
                version=RANCHER_MGMT_VERSION,
                namespace=LOCAL_CLUSTER_ID,
                plural=PROJECT_PLURAL,
                body=body
            )
            return {"success": True, "code": 201, "message": ""}
        except ApiException as e:
            logging(f"Create of project '{display_name}' rejected: "
                    f"status={e.status}: {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def list(self, label_selector=None):
        return list_cr(
            group=RANCHER_MGMT_GROUP,
            version=RANCHER_MGMT_VERSION,
            namespace=LOCAL_CLUSTER_ID,
            plural=PROJECT_PLURAL,
            label_selector=label_selector
        ).get("items", [])

    def get(self, display_name):
        return self.find_by_display_name(self.list(), display_name)

    def get_id(self, display_name):
        project = self.get(display_name)
        if project is None:
            raise AssertionError(f"Project '{display_name}' not found")
        return project["metadata"]["name"]

    def exists(self, display_name):
        try:
            return self.get(display_name) is not None
        except Exception as e:
            logging(f"Error checking project '{display_name}': {e}",
                    'WARNING')
            return False

    def wait_for_active(self, display_name, timeout=DEFAULT_TIMEOUT):
        logging(f"Waiting for project '{display_name}' to be active")
        endtime = datetime.now() + timedelta(seconds=int(timeout))
        project = None
        while endtime > datetime.now():
            try:
                project = self.get(display_name)
                if project and self.is_active(project):
                    logging(f"Project '{display_name}' is active")
                    return project
            except Exception as e:
                logging(f"Error polling project '{display_name}': {e}",
                        'WARNING')
            time.sleep(self.retry_interval)

        conditions = (project or {}).get("status", {}).get("conditions")
        raise AssertionError(
            f"Project '{display_name}' not active within {timeout}s "
            f"(exists={project is not None}, conditions={conditions})"
        )

    def get_resource_limits(self, display_name):
        project = self.get(display_name)
        if project is None:
            raise AssertionError(f"Project '{display_name}' not found")
        return self.extract_limits(project)

    def delete(self, display_name):
        project = self.get(display_name)
        if project is None:
            logging(f"Project '{display_name}' not found, nothing to delete")
            return
        project_id = project["metadata"]["name"]
        logging(f"Deleting project '{display_name}' ({project_id})")
        try:
            delete_cr(
                group=RANCHER_MGMT_GROUP,
                version=RANCHER_MGMT_VERSION,
                namespace=LOCAL_CLUSTER_ID,
                plural=PROJECT_PLURAL,
                name=project_id
            )
        except ApiException as e:
            if e.status != 404:
                raise Exception(
                    f"Failed to delete project '{display_name}': "
                    f"{e.status}, {e.reason}, {e.body}"
                )

    def try_delete(self, display_name):
        project = self.get(display_name)
        if project is None:
            return {"success": False, "code": 404,
                    "message": f"NotFound: project '{display_name}'"}
        try:
            delete_cr(
                group=RANCHER_MGMT_GROUP,
                version=RANCHER_MGMT_VERSION,
                namespace=LOCAL_CLUSTER_ID,
                plural=PROJECT_PLURAL,
                name=project["metadata"]["name"]
            )
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def wait_for_deleted(self, display_name, timeout=DEFAULT_TIMEOUT):
        logging(f"Waiting for project '{display_name}' to be deleted")
        endtime = datetime.now() + timedelta(seconds=int(timeout))
        while endtime > datetime.now():
            if self.get(display_name) is None:
                logging(f"Project '{display_name}' deleted")
                return True
            time.sleep(self.retry_interval)
        raise AssertionError(
            f"Project '{display_name}' still exists after {timeout}s"
        )

    def cleanup(self):
        logging('Cleaning up test projects')
        try:
            projects = self.list(
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )
            for project in projects:
                display_name = project["spec"].get("displayName")
                try:
                    logging(f"Deleting test project: {display_name}")
                    self.delete(display_name)
                except Exception as e:
                    logging(f"Error deleting project {display_name}: {e}",
                            'WARNING')
        except Exception as e:
            logging(f"Error during project cleanup: {e}", 'WARNING')
