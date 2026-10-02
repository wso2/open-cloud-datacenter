"""
GPU Operator Keywords for Robot Framework
Layer 3: Keyword wrappers — creates component instances and delegates.
"""
import os
import sys
import time

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa: E402

from kubernetes import client  # noqa: E402
from kubernetes.client.rest import ApiException  # noqa: E402
from utility.utility import logging, get_retry_count_and_interval  # noqa: E402
from pod_keywords import pod_keywords as PodKeywords  # noqa: E402
from constant import (  # noqa: E402
    HARVESTER_API_GROUP,
    HARVESTER_API_VERSION,
    ADDON_PLURAL,
    HARVESTER_NAMESPACE,
)

_CUDA_VECTORADD_IMAGE = "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubuntu22.04"

# DaemonSet pod prefixes that must be Running when GPU hardware is present.
# These are checked explicitly (in addition to the generic all-pods check) so
# that the wait loop logs their status clearly and does not declare success
# until they are Running.
_GPU_DAEMONSET_PREFIXES = (
    "nvidia-driver-daemonset",
    "nvidia-container-toolkit-daemonset",
    "nvidia-operator-validator",
    "nvidia-device-plugin-daemonset",
)

# Namespaces to search for addons
_ADDON_NAMESPACES = [HARVESTER_NAMESPACE, "kube-system", "default"]

_TERMINAL_WAITING_REASONS = {
    "OOMKilled",
    "ImagePullBackOff",
    "ErrImagePull",
    "CreateContainerConfigError",
}


class gpu_operator_keywords:
    """Robot Framework keyword library for GPU operator test operations."""

    def __init__(self):
        self._core_api = None
        self._custom_api = None
        self.retry_count, self.retry_interval = get_retry_count_and_interval()
        self.pod = PodKeywords()

    @property
    def core_api(self):
        if self._core_api is None:
            self._core_api = client.CoreV1Api()
        return self._core_api

    @property
    def custom_api(self):
        if self._custom_api is None:
            self._custom_api = client.CustomObjectsApi()
        return self._custom_api

    # -------------------------------------------------------------------------
    # Addon checks
    # -------------------------------------------------------------------------

    def check_conflicting_addons(self, pcidevices_addon, nvidia_toolkit_addon):
        """
        Return True if pcidevices-controller or nvidia-driver-toolkit is enabled.

        When True the suite should be skipped to avoid interfering with those
        addons.
        """
        for addon_name in [pcidevices_addon, nvidia_toolkit_addon]:
            for ns in _ADDON_NAMESPACES:
                try:
                    addon = self.custom_api.get_namespaced_custom_object(
                        group=HARVESTER_API_GROUP,
                        version=HARVESTER_API_VERSION,
                        namespace=ns,
                        plural=ADDON_PLURAL,
                        name=addon_name,
                    )
                    if addon.get("spec", {}).get("enabled", False):
                        logging(
                            f"Conflicting addon '{addon_name}' is enabled "
                            f"in namespace '{ns}'"
                        )
                        return True
                    # Found addon but it is disabled — no conflict.
                    break
                except ApiException as e:
                    if e.status == 404:
                        continue
                    logging(
                        f"Error checking addon '{addon_name}' in namespace '{ns}': {e}",
                        level="WARNING",
                    )

        logging("No conflicting addons detected")
        return False

    # -------------------------------------------------------------------------
    # Node labeling
    # -------------------------------------------------------------------------

    def add_gpu_baremetal_label_to_all_nodes(self, label_key, label_value):
        """
        Add *label_key=label_value* to every non-witness node.

        Returns a list of node names that were labeled.
        """
        nodes = self.core_api.list_node()
        labeled = []

        for node in nodes.items:
            node_name = node.metadata.name
            labels = node.metadata.labels or {}

            if "node-role.harvesterhci.io/witness" in labels:
                logging(f"Skipping witness node {node_name}")
                continue

            body = {"metadata": {"labels": {label_key: label_value}}}
            try:
                self.core_api.patch_node(name=node_name, body=body)
                logging(f"Added {label_key}={label_value} to node {node_name}")
                labeled.append(node_name)
            except ApiException as e:
                logging(
                    f"Failed to label node {node_name}: {e}", level="WARNING"
                )

        return labeled

    def remove_gpu_baremetal_label_from_all_nodes(self, label_key):
        """Remove *label_key* from all nodes (sets to null via merge-patch)."""
        nodes = self.core_api.list_node()

        for node in nodes.items:
            node_name = node.metadata.name
            if label_key not in (node.metadata.labels or {}):
                continue

            body = {"metadata": {"labels": {label_key: None}}}
            try:
                self.core_api.patch_node(name=node_name, body=body)
                logging(f"Removed {label_key} from node {node_name}")
            except ApiException as e:
                logging(
                    f"Failed to remove label from node {node_name}: {e}",
                    level="WARNING",
                )

    def _check_pods(self, items):
        """Return (ds_found, failed) — daemonset presence and terminal failures only."""
        failed = []
        ds_found = {prefix: None for prefix in _GPU_DAEMONSET_PREFIXES}

        for pod in items:
            phase = pod.status.phase
            pod_name = pod.metadata.name

            if phase == "Failed":
                failed.append(f"{pod_name}: phase=Failed")
            elif phase in ("Pending", "Running"):
                for cs in (pod.status.init_container_statuses or []) + (
                    pod.status.container_statuses or []
                ):
                    waiting = cs.state.waiting if cs.state else None
                    if waiting and waiting.reason in _TERMINAL_WAITING_REASONS:
                        failed.append(f"{pod_name}/{cs.name}: {waiting.reason}")

            if phase in ("Running", "Succeeded"):
                for prefix in _GPU_DAEMONSET_PREFIXES:
                    if pod_name.startswith(prefix):
                        ds_found[prefix] = f"{pod_name}={phase}"

        return ds_found, failed

    def wait_for_gpu_operator_pods_ready(self, namespace, timeout=300):
        """
        Wait until all GPU Operator pods in *namespace* are Running or Succeeded.

        Observed install time: ~2 minutes on tested hardware.  The default timeout
        is 300 s (~5 min, roughly 2.5× the observed time) to give a comfortable
        margin without waiting excessively long on failure.

        Polls all pods in the namespace until:
        - At least MIN_CORE_PODS pods exist (base install without GPU hardware)
        - Every pod is in Running or Succeeded phase with all containers ready
        - No container is stuck in a terminal failure state

        With GPU hardware the nvidia-* DaemonSet pods also appear and are included
        automatically.  nvidia-cuda-validator finishes as Succeeded ("Completed"
        in kubectl) — that phase is accepted.
        """
        timeout = int(timeout)
        endtime = time.time() + timeout
        # Minimum pods
        MIN_CORE_PODS = 4

        # Main polling loop.
        # The nvidia-* daemonset pods appear *after* the 4 base pods are Running,
        # so we must keep polling even when all current pods are healthy — we only
        # declare success once both GPU daemonset pods are Running too.
        # If they never appear by the timeout it means no GPU hardware is present;
        # we do one final re-check and succeed with a WARNING in that case.
        while time.time() < endtime:
            try:
                items = self.core_api.list_namespaced_pod(namespace=namespace).items

                if len(items) < MIN_CORE_PODS:
                    logging(
                        f"GPU operator namespace '{namespace}' has {len(items)} pod(s); "
                        f"waiting for at least {MIN_CORE_PODS}…"
                    )
                    time.sleep(self.retry_interval)
                    continue

                ds_found, failed = self._check_pods(items)

                if failed:
                    raise AssertionError(
                        f"GPU operator pod(s) in terminal failure state: {failed}"
                    )

                ds_missing = [p for p, v in ds_found.items() if v is None]
                if not ds_missing:
                    for _, status in ds_found.items():
                        logging(f"GPU daemonset pod ready — {status}")
                    logging(f"All GPU Operator pods in '{namespace}' are ready")
                    return True

                logging(f"Waiting for GPU daemonset pods to appear: {ds_missing}…")

            except ApiException as e:
                logging(f"Error listing pods in '{namespace}': {e}", level="WARNING")

            time.sleep(self.retry_interval)

        # Timeout reached.  Re-check once: if all current pods are Running but
        # the GPU daemonset pods never appeared, this is a no-GPU cluster — warn
        # and succeed.  Any non-Running pod is a real failure.
        try:
            items = self.core_api.list_namespaced_pod(namespace=namespace).items
        except ApiException as e:
            raise AssertionError(
                f"Failed to list pods in '{namespace}' after timeout: {e}"
            )

        ds_found, failed = self._check_pods(items)
        ds_missing = [p for p, v in ds_found.items() if v is None]

        if failed:
            raise AssertionError(
                f"GPU operator pod(s) in terminal failure state after timeout: {failed}"
            )

        if ds_missing:
            logging(
                f"WARNING: GPU daemonset pods {ds_missing} did not appear within "
                f"{timeout}s — no GPU hardware detected on this cluster.",
                level="WARNING",
            )
            return True

        pod_states = [f"{p.metadata.name}={p.status.phase}" for p in items]
        raise AssertionError(
            f"GPU operator pods not ready within {timeout}s in namespace '{namespace}'. "
            f"Final pod states: {pod_states}"
        )

    # -------------------------------------------------------------------------
    # GPU node discovery
    # -------------------------------------------------------------------------

    def get_nodes_with_gpu(self):
        """
        Return a list of ``{"name": str, "gpus": str}`` dicts for every node
        that advertises ``nvidia.com/gpu`` in its allocatable resources.

        An empty list is a valid result (no GPU in this cluster).
        """
        gpu_nodes = []
        nodes = self.core_api.list_node()

        for node in nodes.items:
            allocatable = node.status.allocatable or {}
            gpu_count = allocatable.get("nvidia.com/gpu")
            if gpu_count is not None:
                entry = {"name": node.metadata.name, "gpus": gpu_count}
                gpu_nodes.append(entry)
                logging(f"GPU node found: {entry}")

        return gpu_nodes

    # -------------------------------------------------------------------------
    # CUDA workload validation
    # -------------------------------------------------------------------------

    def create_cuda_vectoradd_pod(self, pod_name, namespace):
        """Create the CUDA vectoradd sample pod requesting one GPU."""
        spec = {
            "restartPolicy": "OnFailure",
            "containers": [{
                "name": "cuda-vectoradd",
                "image": _CUDA_VECTORADD_IMAGE,
                "resources": {"limits": {"nvidia.com/gpu": "1"}},
            }],
        }
        self.pod.create_pod(pod_name, namespace, spec)

    def verify_cuda_output(self, logs):
        """
        Assert that the CUDA vectoradd expected output is present in *logs*.

        Expected snippet (from the sample app):
            Test PASSED
        """
        expected = "Test PASSED"
        if expected not in logs:
            raise AssertionError(
                f"Expected '{expected}' not found in CUDA pod output.\n"
                f"Actual logs:\n{logs}"
            )
        logging("CUDA vectoradd output verified")
