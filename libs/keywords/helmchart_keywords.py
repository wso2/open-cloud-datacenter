"""
HelmChart Keywords for Robot Framework
Wraps HelmChart CRD operations for use in .resource files.
"""
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../')))  # noqa: E402

from helmchart import HelmChart  # noqa: E402
from constant import DEFAULT_TIMEOUT_LONG  # noqa: E402

_GPU_OPERATOR_VALUES = """\
cdi:
  nriPluginEnabled: true
driver:
  repository: registry.suse.com/third-party/nvidia
  usePrecompiled: true
  version: 595
"""

_GPU_OPERATOR_CHART_VERSION = "v26.3.2"
_GPU_OPERATOR_CHART_REPO = "https://helm.ngc.nvidia.com/nvidia"


class helmchart_keywords:
    """Robot Framework keyword library for HelmChart CR lifecycle operations."""

    def __init__(self):
        self.helmchart = HelmChart()

    def create_gpu_operator_helmchart(self, name, namespace, target_namespace):
        """Create the GPU Operator HelmChart CR (idempotent)."""
        spec = {
            "chart": "gpu-operator",
            "createNamespace": True,
            "driver": "secret",
            "failurePolicy": "reinstall",
            "repo": _GPU_OPERATOR_CHART_REPO,
            "targetNamespace": target_namespace,
            "valuesContent": _GPU_OPERATOR_VALUES,
            "version": _GPU_OPERATOR_CHART_VERSION,
        }
        self.helmchart.create(name, namespace, spec)

    def wait_for_helmchart_deployed(self, name, namespace, timeout=DEFAULT_TIMEOUT_LONG):
        """Wait until the HelmChart install Job completes successfully."""
        return self.helmchart.wait_for_deployed(name, namespace, timeout)

    def delete_helmchart(self, name, namespace, timeout=DEFAULT_TIMEOUT_LONG):
        """Delete a HelmChart CR and wait for full removal."""
        self.helmchart.delete(name, namespace, timeout)
