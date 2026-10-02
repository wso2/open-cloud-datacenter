"""
Shared Pod utilities for Harvester test framework
"""
from kubernetes import client
from kubernetes.client.rest import ApiException


def get_pods_by_label(namespace, label_selector):
    """Get Pods filtered by label selector

    label_selector example: "app=harvester,component=controller"
    """
    core_api = client.CoreV1Api()
    try:
        pod_list = core_api.list_namespaced_pod(namespace=namespace, label_selector=label_selector)
        return pod_list.items
    except ApiException as e:
        raise Exception(f"Failed to get pods: {e}")
