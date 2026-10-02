"""
Shared CRD utilities for Harvester test framework
"""
import time
from kubernetes import client
from kubernetes.client.rest import ApiException
from utility.utility import logging, get_retry_count_and_interval
from constant import DEFAULT_TIMEOUT_SHORT


def get_cr(group, version, namespace, plural, name):
    """
    Get a Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.get_namespaced_custom_object(
        group=group,
        version=version,
        namespace=namespace,
        plural=plural,
        name=name
    )


def get_cluster_cr(group, version, plural, name):
    """
    Get a cluster-scoped Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.get_cluster_custom_object(
        group=group,
        version=version,
        plural=plural,
        name=name
    )


def create_cr(group, version, namespace, plural, body):
    """
    Create a Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.create_namespaced_custom_object(
        group=group,
        version=version,
        namespace=namespace,
        plural=plural,
        body=body
    )


def create_cluster_cr(group, version, plural, body):
    """
    Create a cluster-scoped Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.create_cluster_custom_object(
        group=group,
        version=version,
        plural=plural,
        body=body
    )


def delete_cr(group, version, namespace, plural, name):
    """
    Delete a Custom Resource
    """
    # An empty name would turn the request into a DELETE on the collection
    # URL, i.e. delete EVERY object of this kind in the namespace.
    assert name, f"refusing to delete {plural} with an empty name"
    obj_api = client.CustomObjectsApi()
    return obj_api.delete_namespaced_custom_object(
        group=group,
        version=version,
        namespace=namespace,
        plural=plural,
        name=name
    )


def delete_cluster_cr(group, version, plural, name):
    """
    Delete a cluster-scoped Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.delete_cluster_custom_object(
        group=group,
        version=version,
        plural=plural,
        name=name
    )


def list_cr(group, version, namespace, plural, label_selector=None):
    """
    List Custom Resources
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.list_namespaced_custom_object(
        group=group,
        version=version,
        namespace=namespace,
        plural=plural,
        label_selector=label_selector
    )


def list_cluster_cr(group, version, plural, label_selector=None):
    """
    List cluster-scoped Custom Resources
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.list_cluster_custom_object(
        group=group,
        version=version,
        plural=plural,
        label_selector=label_selector
    )


def patch_cr(group, version, namespace, plural, name, body):
    """
    Patch a Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.patch_namespaced_custom_object(
        group=group,
        version=version,
        namespace=namespace,
        plural=plural,
        name=name,
        body=body
    )


def patch_cluster_cr(group, version, plural, name, body):
    """
    Patch a cluster-scoped Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.patch_cluster_custom_object(
        group=group,
        version=version,
        plural=plural,
        name=name,
        body=body
    )


def replace_cr(group, version, namespace, plural, name, body):
    """
    Replace a Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.replace_namespaced_custom_object(
        group=group,
        version=version,
        namespace=namespace,
        plural=plural,
        name=name,
        body=body
    )


def replace_cluster_cr(group, version, plural, name, body):
    """
    Replace a cluster-scoped Custom Resource
    """
    obj_api = client.CustomObjectsApi()
    return obj_api.replace_cluster_custom_object(
        group=group,
        version=version,
        plural=plural,
        name=name,
        body=body
    )


def wait_for_cr_deleted(group, version, namespace, plural, name, timeout=DEFAULT_TIMEOUT_SHORT):
    """
    Wait for a Custom Resource to be deleted
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    for i in range(retry_count):
        logging(f"Waiting for {plural}/{name} to be deleted... ({i})")
        try:
            get_cr(group, version, namespace, plural, name)
        except ApiException as e:
            if e.reason == 'Not Found':
                logging(f"Successfully deleted {plural}/{name}")
                return True
        except Exception as e:
            logging(f"Error checking {plural}/{name} deletion: {e}")

        time.sleep(retry_interval)

    raise AssertionError(f"Expected {plural}/{name} to be deleted but it still exists")


def wait_for_cluster_cr_deleted(group, version, plural, name, timeout=DEFAULT_TIMEOUT_SHORT):
    """
    Wait for a cluster-scoped Custom Resource to be deleted
    """
    obj_api = client.CustomObjectsApi()
    retry_count, retry_interval = get_retry_count_and_interval()

    for i in range(retry_count):
        logging(f"Waiting for {plural}/{name} to be deleted... ({i})")
        try:
            obj_api.get_cluster_custom_object(
                group=group,
                version=version,
                plural=plural,
                name=name
            )
        except ApiException as e:
            if e.reason == 'Not Found':
                logging(f"Successfully deleted {plural}/{name}")
                return True
        except Exception as e:
            logging(f"Error checking {plural}/{name} deletion: {e}")

        time.sleep(retry_interval)

    raise AssertionError(f"Expected {plural}/{name} to be deleted but it still exists")


def wait_for_cr_status(group, version, namespace, plural, name, status_field, expected_value, timeout=DEFAULT_TIMEOUT_SHORT):    # NOQA
    """
    Wait for a CR status field to reach expected value
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    cr = None
    for i in range(retry_count):
        logging(f"Waiting for {plural}/{name} {status_field}={expected_value} ({i})...")
        try:
            cr = get_cr(group, version, namespace, plural, name)
            actual_value = cr.get("status", {}).get(status_field)
            if actual_value == expected_value:
                logging(f"{plural}/{name} {status_field}={expected_value}")
                return True
        except Exception as e:
            logging(f"Error getting {plural}/{name} status: {e}")

        time.sleep(retry_interval)

    actual_value = cr.get("status", {}).get(status_field) if cr else None
    raise AssertionError(
        f"Expected {plural}/{name} {status_field}={expected_value}, "
        f"but got {actual_value}"
    )


def wait_for_cluster_cr_status(
    group, version, plural, name, status_field,
    expected_value, timeout=DEFAULT_TIMEOUT_SHORT
):
    """
    Wait for a cluster-scoped CR status field to reach expected value
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    cr = None
    for i in range(retry_count):
        logging(
            f"Waiting for {plural}/{name} "
            f"{status_field}={expected_value} ({i})..."
        )
        try:
            cr = get_cluster_cr(group, version, plural, name)
            actual_value = cr.get("status", {}).get(status_field)
            if actual_value == expected_value:
                logging(f"{plural}/{name} {status_field}={expected_value}")
                return True
        except Exception as e:
            logging(f"Error getting {plural}/{name} status: {e}")

        time.sleep(retry_interval)

    actual_value = cr.get("status", {}).get(status_field) if cr else None
    raise AssertionError(
        f"Expected {plural}/{name} {status_field}={expected_value}, "
        f"but got {actual_value}"
    )


def wait_for_cr_condition(
    group, version, namespace, plural, name,
    condition_type, condition_status, timeout=DEFAULT_TIMEOUT_SHORT
):
    """
    Wait for a CR condition to reach expected status
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    cr = None
    for i in range(retry_count):
        logging(
            f"Waiting for {plural}/{name} condition "
            f"{condition_type}={condition_status} ({i})..."
        )
        try:
            cr = get_cr(group, version, namespace, plural, name)
            conditions = cr.get("status", {}).get("conditions", [])
            for condition in conditions:
                type_match = condition.get("type", "").lower() == condition_type.lower()
                status_match = condition.get("status", "").lower() == condition_status.lower()
                if type_match and status_match:
                    logging(f"{plural}/{name} condition {condition_type}={condition_status}")
                    return True
        except Exception as e:
            logging(f"Error getting {plural}/{name} conditions: {e}")

        time.sleep(retry_interval)

    raise AssertionError(
        f"Failed to wait for {plural}/{name} condition {condition_type}={condition_status}"
    )


def wait_for_cluster_cr_condition(
    group, version, plural, name,
    condition_type, condition_status, timeout=DEFAULT_TIMEOUT_SHORT
):
    """
    Wait for a cluster-scoped CR condition to reach expected status
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    cr = None
    for i in range(retry_count):
        logging(
            f"Waiting for {plural}/{name} condition "
            f"{condition_type}={condition_status} ({i})..."
        )
        try:
            cr = get_cluster_cr(group, version, plural, name)
            conditions = cr.get("status", {}).get("conditions", [])
            for condition in conditions:
                type_match = (
                    condition.get("type", "").lower()
                    == condition_type.lower()
                )
                status_match = (
                    condition.get("status", "").lower()
                    == condition_status.lower()
                )
                if type_match and status_match:
                    logging(
                        f"{plural}/{name} condition "
                        f"{condition_type}={condition_status}"
                    )
                    return True
        except Exception as e:
            logging(f"Error getting {plural}/{name} conditions: {e}")

        time.sleep(retry_interval)

    raise AssertionError(
        f"Failed to wait for {plural}/{name} condition "
        f"{condition_type}={condition_status}"
    )


def set_cr_annotation(group, version, namespace, plural, name, annotation_key, annotation_value):
    """
    Set an annotation on a Custom Resource
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    for i in range(retry_count):
        try:
            cr = get_cr(group, version, namespace, plural, name)
            annotations = cr.get('metadata', {}).get('annotations', {})
            annotations[annotation_key] = annotation_value
            if 'metadata' not in cr:
                cr['metadata'] = {}
            cr['metadata']['annotations'] = annotations

            replace_cr(group, version, namespace, plural, name, cr)
            logging(f"Set annotation {annotation_key}={annotation_value} on {plural}/{name}")
            return
        except ApiException as e:
            if e.status == 409:  # Conflict error
                logging(f"Conflict error when setting annotation, retry ({i})...")
            else:
                raise e

        time.sleep(retry_interval)

    raise AssertionError(
        f"Failed to set annotation on {plural}/{name} "
        f"after {retry_count} retries"
    )


def set_cluster_cr_annotation(
    group, version, plural, name, annotation_key, annotation_value
):
    """
    Set an annotation on a cluster-scoped Custom Resource
    """
    retry_count, retry_interval = get_retry_count_and_interval()

    for i in range(retry_count):
        try:
            cr = get_cluster_cr(group, version, plural, name)
            annotations = cr.get('metadata', {}).get('annotations', {})
            annotations[annotation_key] = annotation_value
            if 'metadata' not in cr:
                cr['metadata'] = {}
            cr['metadata']['annotations'] = annotations

            replace_cluster_cr(group, version, plural, name, cr)
            logging(
                f"Set annotation {annotation_key}={annotation_value} "
                f"on {plural}/{name}"
            )
            return
        except ApiException as e:
            if e.status == 409:
                logging(
                    f"Conflict error when setting annotation, retry ({i})..."
                )
            else:
                raise e

        time.sleep(retry_interval)

    raise AssertionError(
        f"Failed to set annotation on {plural}/{name} "
        f"after {retry_count} retries"
    )


def get_cr_annotation_value(group, version, namespace, plural, name, annotation_key):
    """
    Get an annotation value from a Custom Resource
    """
    cr = get_cr(group, version, namespace, plural, name)
    return cr.get('metadata', {}).get('annotations', {}).get(annotation_key)


def get_cluster_cr_annotation_value(
    group, version, plural, name, annotation_key
):
    """
    Get an annotation value from a cluster-scoped Custom Resource
    """
    cr = get_cluster_cr(group, version, plural, name)
    return cr.get('metadata', {}).get('annotations', {}).get(annotation_key)


def convert_size_to_bytes(size_str):
    """
    Convert size string (like '10Gi', '5Mi') to bytes
    """
    from constant import KIBIBYTE, MEBIBYTE, GIBIBYTE

    if isinstance(size_str, int):
        return size_str

    size_str = str(size_str).strip()

    if size_str.endswith('Gi'):
        return int(float(size_str[:-2]) * GIBIBYTE)
    elif size_str.endswith('Mi'):
        return int(float(size_str[:-2]) * MEBIBYTE)
    elif size_str.endswith('Ki'):
        return int(float(size_str[:-2]) * KIBIBYTE)
    elif size_str.endswith('G'):
        return int(float(size_str[:-1]) * GIBIBYTE)
    elif size_str.endswith('M'):
        return int(float(size_str[:-1]) * MEBIBYTE)
    elif size_str.endswith('K'):
        return int(float(size_str[:-1]) * KIBIBYTE)
    else:
        return int(size_str)
