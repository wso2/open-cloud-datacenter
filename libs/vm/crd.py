"""
VM CRD Implementation
"""
import json
import time
from kubernetes import client
from kubernetes.client.rest import ApiException
from crd import get_cr, create_cr, delete_cr, list_cr, wait_for_cr_deleted, replace_cr
from constant import (
    KUBEVIRT_API_GROUP, KUBEVIRT_API_VERSION,
    VIRTUALMACHINE_PLURAL, VIRTUALMACHINEINSTANCE_PLURAL,
    DEFAULT_NAMESPACE, LABEL_TEST, LABEL_TEST_VALUE,
    DEFAULT_TIMEOUT_SHORT, DEFAULT_STORAGE_CLASS,
    RUN_STRATEGY_RERUN_ON_FAILURE, RUN_STRATEGY_HALTED,
)
from utility.utility import logging, get_retry_count_and_interval
from vm.base import Base


class CRD(Base):
    """
    VM CRD implementation - uses Kubernetes Custom Resources
    """

    def __init__(self):
        """Initialize CRD client."""
        self.obj_api = client.CustomObjectsApi()
        self.core_api = client.CoreV1Api()
        self.retry_count, self.retry_interval = (
            get_retry_count_and_interval()
        )

    def create(self, vm_name, image_id, cpu, memory, **kwargs):
        """Create a VM using CRD"""
        namespace = kwargs.get('namespace', DEFAULT_NAMESPACE)

        try:
            logging(f"Creating VirtualMachine {namespace}/{vm_name}")
            self._create_virtual_machine(
                vm_name, image_id, cpu, memory, namespace, **kwargs
            )

            # Wait for VM to be created
            self.wait_for_vm_created(vm_name, namespace)

            logging(f"VM {namespace}/{vm_name} created successfully")

            return {'metadata': {'name': vm_name, 'namespace': namespace}}

        except Exception as e:
            logging(f"Failed to create VM {vm_name}: {e}")
            raise

    def _create_virtual_machine(
            self, vm_name, image_id, cpu, memory, namespace, **kwargs):
        """Create VM matching Harvester's exact structure.

        extra_disks: optional list of dicts passed via kwargs, each dict may contain:
            - size: required, e.g. "10Gi"
            - storage_class: optional, defaults to "harvester-longhorn"
            - name: optional, defaults to "{vm_name}-disk-{index}"
        Example:
            extra_disks=[
                {"size": "20Gi", "storage_class": "sc-lhv2"},
                {"name": "data-2", "size": "50Gi"}
            ]
        """

        # Look up the image's actual storage class from Harvester.
        # Since v1.8.0 (harvester#5165), storage classes use lh-<uid>
        # instead of longhorn-<image_name>. We read status.storageClassName
        # which works for both old and new Harvester versions.
        try:
            img_obj = self.obj_api.get_namespaced_custom_object(
                group="harvesterhci.io",
                version="v1beta1",
                namespace=namespace,
                plural="virtualmachineimages",
                name=image_id
            )
            storage_class = img_obj.get("status", {}).get(
                "storageClassName", ""
            )
            if not storage_class:
                raise Exception(
                    f"Image {image_id} has no storageClassName in status"
                )
            logging(f"Resolved storage class for image {image_id}: "
                    f"{storage_class}")
        except ApiException as e:
            raise Exception(
                f"Failed to look up image {image_id} for storage class: {e}"
            )

        # Build volumeClaimTemplates annotation for Harvester
        disk_name = "disk-0"
        volume_name = f"{vm_name}-{disk_name}"
        boot_disk_size = kwargs.get("disk_size", "10Gi")
        volume_claim_templates = [
            {
                "metadata": {
                    "name": volume_name,
                    "annotations": {
                        "harvesterhci.io/imageId": f"{namespace}/{image_id}"
                    },
                    "labels": {
                        LABEL_TEST: LABEL_TEST_VALUE
                    }
                },
                "spec": {
                    "accessModes": ["ReadWriteMany"],
                    "resources": {
                        "requests": {
                            "storage": boot_disk_size
                        }
                    },
                    "volumeMode": "Block",
                    "storageClassName": storage_class
                }
            }
        ]
        spec_devices_disks = [
            {
                "bootOrder": 1,
                "name": disk_name,
                "disk": {
                    "bus": "virtio"
                }
            }
        ]
        spec_volumes = [
            {
                "name": disk_name,
                "persistentVolumeClaim": {
                    "claimName": volume_name
                }
            }
        ]

        # runStrategy controls whether the VM boots on create. Default keeps the
        # existing behaviour (RerunOnFailure starts it); pass run_strategy=Halted
        # to create a stopped VM.
        run_strategy = kwargs.get("run_strategy", RUN_STRATEGY_RERUN_ON_FAILURE)
        # The vmRunStrategy annotation must hold the last *running* strategy —
        # Harvester's mutator restores spec.runStrategy from it whenever the VM
        # leaves Halted, so "Halted" there would make the VM unstartable.
        annotation_run_strategy = (
            RUN_STRATEGY_RERUN_ON_FAILURE if run_strategy == RUN_STRATEGY_HALTED
            else run_strategy
        )

        extra_disks = kwargs.get("extra_disks", [])
        if extra_disks:
            if not isinstance(extra_disks, list):
                raise Exception("extra_disks must be a list of dicts")
            for idx, disk in enumerate(extra_disks, start=1):
                volume_name = disk.get("name", f"{vm_name}-disk-{idx}")
                volume_claim_templates.append(
                    {
                        "metadata": {
                            "name": volume_name,
                            "labels": {
                                LABEL_TEST: LABEL_TEST_VALUE
                            }
                        },
                        "spec": {
                            "accessModes": ["ReadWriteMany"],
                            "resources": {
                                "requests": {
                                    "storage": disk["size"]
                                }
                            },
                            "volumeMode": "Block",
                            "storageClassName": disk.get("storage_class", DEFAULT_STORAGE_CLASS)
                        }
                    }
                )
                spec_devices_disks.append(
                    {
                        "name": volume_name,
                        "disk": {
                            "bus": "virtio"
                        }
                    }
                )
                spec_volumes.append(
                    {
                        "name": volume_name,
                        "persistentVolumeClaim": {
                            "claimName": volume_name
                        }
                    }
                )

        # Attach pre-existing PVCs (not provisioned via volumeClaimTemplates).
        # existing_volumes: list of PVC names already Bound in the namespace.
        existing_volumes = kwargs.get("existing_volumes", [])
        if existing_volumes:
            if not isinstance(existing_volumes, list):
                raise Exception("existing_volumes must be a list of PVC names")
            for idx, pvc_name in enumerate(existing_volumes, start=1):
                disk_nm = f"existing-{idx}"
                spec_devices_disks.append(
                    {"name": disk_nm, "disk": {"bus": "virtio"}}
                )
                spec_volumes.append(
                    {"name": disk_nm,
                     "persistentVolumeClaim": {"claimName": pvc_name}}
                )

        logging(f"Volume claim templates for VM {vm_name}: {volume_claim_templates}")
        logging(f"Volume spec_devices_disks for VM {vm_name}: {spec_devices_disks}")

        # Optional cloud-init NoCloud disk: if ssh_public_key is provided,
        # authorizes it for guest SSH access (used by exec_command_in_vm /
        # Execute Command In VM). Without this the VM has no way to log in
        # with the generated guest keypair.
        ssh_public_key = kwargs.get("ssh_public_key")
        if ssh_public_key:
            spec_devices_disks.append(
                {"name": "cloudinitdisk", "disk": {"bus": "virtio"}}
            )
            spec_volumes.append(
                {
                    "name": "cloudinitdisk",
                    "cloudInitNoCloud": {
                        "networkData": "",
                        "userData": (
                            "#cloud-config\n"
                            f"ssh_authorized_keys:\n  - {ssh_public_key}\n"
                        )
                    }
                }
            )

        # Build complete VM spec matching Harvester structure
        body = {
            "apiVersion": f"{KUBEVIRT_API_GROUP}/{KUBEVIRT_API_VERSION}",
            "kind": "VirtualMachine",
            "metadata": {
                "name": vm_name,
                "namespace": namespace,
                "annotations": {
                    "harvesterhci.io/vmRunStrategy": annotation_run_strategy,
                    "harvesterhci.io/volumeClaimTemplates": (
                        json.dumps(volume_claim_templates)
                    ),
                    "harvesterhci.io/sshNames": "[]"
                },
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE,
                    "harvesterhci.io/creator": "robot-framework",
                    "harvesterhci.io/os": "linux"
                }
            },
            "spec": {
                "runStrategy": run_strategy,
                "template": {
                    "metadata": {
                        "annotations": {
                            "harvesterhci.io/sshNames": "[]"
                        },
                        "labels": {
                            "harvesterhci.io/vmName": vm_name
                        }
                    },
                    "spec": {
                        "affinity": {},
                        "architecture": "amd64",
                        "domain": {
                            "cpu": {
                                "cores": int(cpu),
                                "sockets": 1,
                                "threads": 1
                            },
                            "devices": {
                                "disks": spec_devices_disks,
                                "inputs": [
                                    {
                                        "bus": "usb",
                                        "name": "tablet",
                                        "type": "tablet"
                                    }
                                ],
                                "interfaces": [
                                    {
                                        "masquerade": {},
                                        "model": "virtio",
                                        "name": "default"
                                    }
                                ]
                            },
                            "features": {
                                "acpi": {"enabled": True}
                            },
                            "machine": {"type": "q35"},
                            "memory": {
                                "guest": str(memory)
                            },
                            "resources": {
                                "limits": {
                                    "cpu": str(cpu),
                                    "memory": str(memory)
                                },
                                "requests": {
                                    "cpu": kwargs.get("requests_cpu", "125m"),
                                    "memory": kwargs.get("requests_memory", "2730Mi")
                                }
                            }
                        },
                        "evictionStrategy": "LiveMigrateIfPossible",
                        "hostname": vm_name,
                        "networks": [
                            {"name": "default", "pod": {}}
                        ],
                        "volumes": spec_volumes,
                        "terminationGracePeriodSeconds": 120
                    }
                }
            }
        }
        logging(f"Creating VM with spec: {body}")

        try:
            create_cr(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINE_PLURAL,
                body=body
            )
        except ApiException as e:
            logging(f"Failed to create VirtualMachine: {e}")
            raise

    def delete(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Delete a VM together with the PVCs it owns (boot + data disks).

        Harvester does not cascade-delete the volumeClaimTemplate PVCs, so we
        collect the PVC claim names from the VM spec, delete the VM, wait for it
        to be gone, then delete those PVCs. This also unblocks deleting the
        source image afterwards (its backing image can't be removed while a PVC
        still references it).
        """
        # Collect the PVC names this VM references, before it disappears.
        pvc_names = []
        try:
            vm = self.get(vm_name, namespace)
            volumes = (vm.get('spec', {}).get('template', {}).get('spec', {})
                       .get('volumes', []))
            for vol in volumes:
                claim = vol.get('persistentVolumeClaim', {}).get('claimName')
                if claim:
                    pvc_names.append(claim)
        except ApiException as e:
            if e.status != 404:
                logging(f"Could not read VM {vm_name} volumes before delete: {e}",
                        "WARNING")

        # Delete the VM.
        try:
            logging(f"Deleting VirtualMachine {namespace}/{vm_name}")
            delete_cr(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINE_PLURAL,
                name=vm_name
            )
            logging(f"Deleted VirtualMachine {namespace}/{vm_name}")
        except ApiException as e:
            if e.status != 404:
                logging(f"Error deleting VM {vm_name}: {e}")
                raise

        # Wait for the VM to be gone so its PVCs are released, then delete them.
        try:
            self.wait_for_deleted(vm_name, DEFAULT_TIMEOUT_SHORT, namespace)
        except Exception as e:
            logging(f"VM {vm_name} not fully gone before PVC cleanup: {e}",
                    "WARNING")

        for pvc in pvc_names:
            try:
                logging(f"Deleting PVC {namespace}/{pvc} owned by VM {vm_name}")
                self.core_api.delete_namespaced_persistent_volume_claim(
                    name=pvc, namespace=namespace
                )
            except ApiException as e:
                if e.status != 404:
                    logging(f"Error deleting PVC {pvc}: {e}", "WARNING")

        # PVC deletion is asynchronous (Longhorn/CSI finalizers keep the
        # object around briefly after the delete call returns), so wait for
        # each one to actually disappear. Without this, a caller that
        # deletes the backing image right after VM is deleted can race
        # ahead of the PVC teardown and get rejected by Harvester's
        # validating webhook ("Cannot delete image ...: being used by
        # volume ...").
        for pvc in pvc_names:
            self._wait_for_pvc_deleted(pvc, namespace)

    def _wait_for_pvc_deleted(self, pvc_name, namespace=DEFAULT_NAMESPACE,
                              timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait until a PVC is fully gone."""
        endtime = time.time() + timeout
        while time.time() < endtime:
            try:
                self.core_api.read_namespaced_persistent_volume_claim(
                    name=pvc_name, namespace=namespace
                )
            except ApiException as e:
                if e.status == 404:
                    logging(f"PVC {namespace}/{pvc_name} is deleted")
                    return
                logging(f"Error checking PVC {pvc_name} deletion: {e}", "WARNING")
                return
            time.sleep(self.retry_interval)
        logging(f"PVC {namespace}/{pvc_name} was not deleted within {timeout}s",
                "WARNING")

    def get(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Get VirtualMachine."""
        return get_cr(
            group=KUBEVIRT_API_GROUP,
            version=KUBEVIRT_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINE_PLURAL,
            name=vm_name
        )

    def list(self, namespace=DEFAULT_NAMESPACE, label_selector=None):
        """List VirtualMachines."""
        result = list_cr(
            group=KUBEVIRT_API_GROUP,
            version=KUBEVIRT_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINE_PLURAL,
            label_selector=label_selector
        )
        return result.get("items", [])

    def start(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Start a VM via the kubevirt `start` subresource.

        This is the same path Harvester itself uses (dashboard and the
        restore controller's startVM). The subresource issues a state-change
        request, which boots the VM regardless of its run strategy — a
        spec-only runStrategy update is NOT enough: the strategy restored
        from the vmRunStrategy annotation is usually RerunOnFailure, and
        under RerunOnFailure a cleanly shut down VMI (phase Succeeded) is
        never restarted, so the VM would hang stopped forever. Harvester's
        VM mutator then restores spec.runStrategy from the annotation, so
        the VM comes back with its original strategy.
        """
        logging(f"Starting VM {namespace}/{vm_name}")
        try:
            self._vm_subresource(vm_name, "start", namespace)
        except ApiException as e:
            raise AssertionError(
                f"Failed to start VM {namespace}/{vm_name}: "
                f"{e.status} {e.reason} {e.body}"
            )
        logging(f"VM {namespace}/{vm_name} started")

    def stop(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Stop a VM."""
        for i in range(self.retry_count):
            try:
                vm = self.get(vm_name, namespace)
                vm['spec']['runStrategy'] = 'Halted'
                # Do NOT write the harvesterhci.io/vmRunStrategy annotation here.
                # Harvester keeps the last *running* strategy in that annotation
                # and its VM mutator restores spec.runStrategy from it on any
                # Halted -> non-Halted transition. Writing "Halted" into it makes
                # every Harvester-initiated start (restore, node maintenance) a
                # no-op, e.g. a replace-restore hangs at "Waiting for target vm
                # to be ready". The mutator only intervenes when *leaving*
                # Halted, so a plain spec change is enough to stop the VM.

                self.obj_api.replace_namespaced_custom_object(
                    group=KUBEVIRT_API_GROUP,
                    version=KUBEVIRT_API_VERSION,
                    namespace=namespace,
                    plural=VIRTUALMACHINE_PLURAL,
                    name=vm_name,
                    body=vm
                )

                logging(f"VM {namespace}/{vm_name} stopped")
                return

            except ApiException as e:
                if e.status == 409:
                    logging(f"Conflict when stopping VM, retrying ({i})...")
                    time.sleep(self.retry_interval)
                else:
                    raise

        raise AssertionError(
            f"Failed to stop VM after {self.retry_count} attempts"
        )

    def restart(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Restart a VM by stopping then starting it.

        Implemented as stop + wait_for_stopped + start (pure CRD, reliable).
        start() restores the run strategy remembered in the vmRunStrategy
        annotation, so the VM comes back with its original strategy.

        Unlike the REST implementation's restart() -- which calls
        KubeVirt's native `restart` subresource action and then has to
        diff the VMI's `metadata.uid` before/after to detect that a
        genuinely new VMI came up (the old VMI can still be observed
        reporting phase=Running with its old IP for a moment after
        `restart` is triggered) -- no such check is needed here. That's
        because wait_for_stopped() below requires BOTH
        status.printableStatus == "Stopped" AND the VMI object being
        completely gone (_vmi_is_gone()) before this method ever calls
        start(). So by the time start() runs, the old VMI is already
        confirmed deleted, and any VMI wait_for_running() subsequently
        observes as Running is necessarily the new one -- there is no
        window where a stale old VMI could be misidentified as the new
        one, so comparing UIDs would be redundant here.
        """
        logging(f"Restarting VM {namespace}/{vm_name} via stop+start")
        self.stop(vm_name, namespace)
        self.wait_for_stopped(vm_name, DEFAULT_TIMEOUT_SHORT, namespace)
        self.start(vm_name, namespace)
        logging(f"VM {namespace}/{vm_name} restart initiated")

    def _vmi_is_gone(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return True when the VMI object no longer exists."""
        try:
            self.obj_api.get_namespaced_custom_object(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEINSTANCE_PLURAL,
                name=vm_name
            )
            return False
        except ApiException as e:
            if e.status == 404:
                return True
            raise

    def _vmi_subresource(self, vm_name, action, namespace=DEFAULT_NAMESPACE, body=None):
        """Call a kubevirt VMI subresource (pause/unpause) via the aggregated
        subresources.kubevirt.io API. These have no spec-field equivalent, so
        they must go through the subresource endpoint.
        """
        path = (f"/apis/subresources.{KUBEVIRT_API_GROUP}/{KUBEVIRT_API_VERSION}"
                f"/namespaces/{namespace}/{VIRTUALMACHINEINSTANCE_PLURAL}"
                f"/{vm_name}/{action}")
        return self.obj_api.api_client.call_api(
            path, "PUT",
            header_params={"Content-Type": "application/json"},
            body=body if body is not None else {},
            auth_settings=["BearerToken"],
            response_type=None,
            _return_http_data_only=True,
            _preload_content=False,
        )

    def pause(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Pause a running VM (VMI pause subresource)."""
        logging(f"Pausing VM {namespace}/{vm_name}")
        self._vmi_subresource(vm_name, "pause", namespace)

    def unpause(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Unpause a paused VM (VMI unpause subresource)."""
        logging(f"Unpausing VM {namespace}/{vm_name}")
        self._vmi_subresource(vm_name, "unpause", namespace)

    def softreboot(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Soft-reboot a running VM (VMI softreboot subresource).

        This is the same aggregated subresources.kubevirt.io endpoint that
        Harvester's `vms.softreboot()` API action proxies to (action=
        "softreboot"). It asks the guest agent to reboot the guest OS in
        place, unlike restart()/stop()+start() which tear down and recreate
        the VMI. Requires the guest agent to be connected; kubevirt returns
        an error if it is not.
        """
        logging(f"Soft-rebooting VM {namespace}/{vm_name}")
        self._vmi_subresource(vm_name, "softreboot", namespace)

    def wait_vm_condition(
            self, vm_name, condition_type, condition_status,
            timeout=DEFAULT_TIMEOUT_SHORT, namespace=DEFAULT_NAMESPACE,
            match_field="type"):
        """Wait until a VM's status.conditions reaches the expected state.

        status.conditions is a set keyed by `type`, NOT an append-ordered
        log -- entries such as Ready/LiveMigratable/StorageLiveMigratable
        are always present, while conditions like AgentConnected only
        appear once qemu-guest-agent connects.

        If `condition_status` is a string (e.g. "True"), waits until a
        condition matching `condition_type` (compared against the
        condition's `match_field`, either "type" or "reason") exists with
        a matching `status`, and returns that condition dict (so callers
        can still read fields such as `lastProbeTime`).

        If `condition_status` is None, waits until NO matching condition
        is present at all, and returns None.
        """
        for i in range(timeout):
            vm = self.get(vm_name, namespace)
            conditions = vm.get('status', {}).get('conditions', [])
            cond = next((c for c in conditions if c.get(match_field) == condition_type), None)

            if condition_status is None:
                if cond is None:
                    logging(f"VM {namespace}/{vm_name} condition {condition_type} is absent")
                    return None
            elif cond is not None and cond.get('status') == condition_status:
                logging(f"VM {namespace}/{vm_name} condition {condition_type}="
                        f"{condition_status}")
                return cond

            if i % 30 == 0:
                logging(f"Waiting for VM {vm_name} condition {condition_type}="
                        f"{condition_status} (current: {cond})...")
            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {namespace}/{vm_name} condition {condition_type} did not reach "
            f"status={condition_status} within {timeout}s"
        )

    def wait_for_agent_connected(
            self, vm_name, timeout=DEFAULT_TIMEOUT_SHORT, namespace=DEFAULT_NAMESPACE):
        """Wait until the VM's guest agent connects.

        Right after a VM is created/started, qemu-guest-agent has not
        connected yet (only Ready/LiveMigratable/StorageLiveMigratable
        exist), so this must be polled rather than checked once. Returns the
        AgentConnected condition dict (includes `lastProbeTime`).
        """
        return self.wait_vm_condition(vm_name, "AgentConnected", "True", timeout, namespace)

    def wait_for_agent_disconnected(
            self, vm_name, timeout=DEFAULT_TIMEOUT_SHORT, namespace=DEFAULT_NAMESPACE):
        """Wait until the VM's guest agent disconnects (AgentConnected
        condition is removed entirely)."""
        return self.wait_vm_condition(vm_name, "AgentConnected", None, timeout, namespace)

    def wait_for_paused(self, vm_name, timeout=DEFAULT_TIMEOUT_SHORT,
                        namespace=DEFAULT_NAMESPACE):
        """Wait for the VMI to report the Paused condition as True."""
        endtime = time.time() + timeout
        while time.time() < endtime:
            try:
                vmi = self.obj_api.get_namespaced_custom_object(
                    group=KUBEVIRT_API_GROUP,
                    version=KUBEVIRT_API_VERSION,
                    namespace=namespace,
                    plural=VIRTUALMACHINEINSTANCE_PLURAL,
                    name=vm_name
                )
                conditions = vmi.get('status', {}).get('conditions', [])
                if any(c.get('type') == 'Paused' and c.get('status') == 'True'
                       for c in conditions):
                    logging(f"VM {namespace}/{vm_name} is paused")
                    return True
            except ApiException as e:
                if e.status != 404:
                    logging(f"Error checking VM paused status: {e}")
            time.sleep(self.retry_interval)
        raise AssertionError(
            f"VM {namespace}/{vm_name} was not paused within {timeout}s")

    def try_get(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Attempt to get a VM; returns {success, code, message}."""
        try:
            self.get(vm_name, namespace)
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def try_delete(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Attempt to delete a VM; returns {success, code, message}.

        Unlike delete(), does NOT swallow a 404 so callers can assert that
        deleting a missing VM is rejected.
        """
        try:
            delete_cr(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINE_PLURAL,
                name=vm_name
            )
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            logging(f"Delete of VM {namespace}/{vm_name} returned "
                    f"status={e.status}: {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def migrate(self, vm_name, target_node, namespace=DEFAULT_NAMESPACE):
        """Migrate VM to target node."""
        logging(
            f"Migrating VM {namespace}/{vm_name} to node {target_node}"
        )
        try:
            # Create VirtualMachineInstanceMigration object
            migration_body = {
                "apiVersion": f"{KUBEVIRT_API_GROUP}/{KUBEVIRT_API_VERSION}",
                "kind": "VirtualMachineInstanceMigration",
                "metadata": {
                    "name": f"{vm_name}-migration",
                    "namespace": namespace
                },
                "spec": {
                    "vmiName": vm_name
                }
            }

            self.obj_api.create_namespaced_custom_object(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural="virtualmachineinstancemigrations",
                body=migration_body
            )
            logging(
                f"VM {namespace}/{vm_name} migration to "
                f"{target_node} initiated"
            )
        except ApiException as e:
            logging(f"Error migrating VM: {e}")
            raise

    def get_node(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return the node currently running the VM, or None when not running.

        The authoritative source is the VMI's status.nodeName; the VM object does
        not carry placement information.
        """
        try:
            vmi = self.obj_api.get_namespaced_custom_object(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEINSTANCE_PLURAL,
                name=vm_name
            )
            return vmi.get('status', {}).get('nodeName')
        except ApiException as e:
            if e.status == 404:
                logging(f"VMI {namespace}/{vm_name} not found; VM is not running")
                return None
            raise

    def verify_on_node(self, vm_name, expected_node, namespace=DEFAULT_NAMESPACE):
        """Assert the VM is currently running on expected_node."""
        actual_node = self.get_node(vm_name, namespace)
        assert actual_node == expected_node, \
            f"VM {namespace}/{vm_name} is on {actual_node}, expected {expected_node}"

    def wait_for_migrated_away_from(
            self, vm_name, original_node, timeout=DEFAULT_TIMEOUT_SHORT,
            namespace=DEFAULT_NAMESPACE):
        """Wait until the VM is Running on any node other than original_node.

        Used for descheduler tests, where the destination is chosen by the
        scheduler rather than requested by the test.

        Returns:
            str: The node the VM ended up on
        """
        logging(
            f"Waiting for VM {namespace}/{vm_name} to leave node {original_node}"
        )
        endtime = time.time() + timeout
        while time.time() < endtime:
            try:
                vmi = self.obj_api.get_namespaced_custom_object(
                    group=KUBEVIRT_API_GROUP,
                    version=KUBEVIRT_API_VERSION,
                    namespace=namespace,
                    plural=VIRTUALMACHINEINSTANCE_PLURAL,
                    name=vm_name
                )
                status = vmi.get('status', {})
                current_node = status.get('nodeName')
                if (current_node and current_node != original_node
                        and status.get('phase') == 'Running'):
                    logging(
                        f"VM {namespace}/{vm_name} moved from {original_node} "
                        f"to {current_node}"
                    )
                    return current_node
            except ApiException as e:
                if e.status != 404:
                    logging(f"Error reading VMI: {e}", level='WARNING')

            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {namespace}/{vm_name} did not move off node {original_node} "
            f"within {timeout}s"
        )

    def get_annotations(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return the VM object's metadata.annotations."""
        vm = self.get(vm_name, namespace)
        return vm.get('metadata', {}).get('annotations', {}) or {}

    def wait_for_vm_created(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Wait for VM CR creation."""
        for i in range(self.retry_count):
            try:
                self.get(vm_name, namespace)
                logging(f"VirtualMachine {namespace}/{vm_name} created")
                return True
            except Exception:
                logging(f"Waiting for VM to be created ({i})...")
                time.sleep(self.retry_interval)

        raise AssertionError(
            f"VirtualMachine {namespace}/{vm_name} not created"
        )

    def wait_for_running(
            self, vm_name, timeout=DEFAULT_TIMEOUT_SHORT, namespace=DEFAULT_NAMESPACE):
        """Wait for VM to be running."""
        endtime = time.time() + timeout
        last_log_time = 0

        while time.time() < endtime:
            try:
                vmi = self.obj_api.get_namespaced_custom_object(
                    group=KUBEVIRT_API_GROUP,
                    version=KUBEVIRT_API_VERSION,
                    namespace=namespace,
                    plural=VIRTUALMACHINEINSTANCE_PLURAL,
                    name=vm_name
                )

                phase = vmi.get('status', {}).get('phase', 'Unknown')

                if phase == 'Running':
                    logging(f"VM {namespace}/{vm_name} is running")
                    return True

                now = time.time()
                if now - last_log_time >= 30:
                    logging(
                        f"Waiting for VM {vm_name} to run (current phase: {phase})..."
                    )
                    last_log_time = now

            except ApiException as e:
                if e.status != 404:
                    logging(f"Error checking VM status: {e}")

            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {namespace}/{vm_name} did not run within {timeout}s\n"
            f"{self._describe_vm_startup(vm_name, namespace)}"
        )

    def _describe_vm_startup(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Collect why a VM is not running: VMI phase/conditions plus the
        virt-launcher pod's status and pending events (unschedulable,
        volume attach, image pull, ...). Used to make startup-timeout
        failures diagnosable from the test log alone.
        """
        lines = []
        try:
            vmi = self.obj_api.get_namespaced_custom_object(
                group=KUBEVIRT_API_GROUP,
                version=KUBEVIRT_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEINSTANCE_PLURAL,
                name=vm_name
            )
            status = vmi.get('status', {})
            lines.append(f"VMI phase: {status.get('phase', 'Unknown')}")
            for cond in status.get('conditions', []):
                lines.append(f"VMI condition: {cond.get('type')}="
                             f"{cond.get('status')} {cond.get('reason', '')} "
                             f"{cond.get('message', '')}".rstrip())
        except Exception as e:
            lines.append(f"VMI not readable: {e}")

        try:
            pods = self.core_api.list_namespaced_pod(
                namespace,
                label_selector=f"vm.kubevirt.io/name={vm_name}"
            )
            for pod in pods.items:
                lines.append(f"Pod {pod.metadata.name}: "
                             f"phase={pod.status.phase}")
                for cond in (pod.status.conditions or []):
                    if cond.status != 'True':
                        lines.append(f"  condition {cond.type}={cond.status} "
                                     f"{cond.reason or ''}: "
                                     f"{cond.message or ''}".rstrip())
                events = self.core_api.list_namespaced_event(
                    namespace,
                    field_selector=(f"involvedObject.name={pod.metadata.name},"
                                    f"involvedObject.kind=Pod")
                )
                for ev in events.items[-5:]:
                    lines.append(f"  event {ev.type}/{ev.reason}: {ev.message}")
        except Exception as e:
            lines.append(f"virt-launcher pod not readable: {e}")

        return "\n".join(lines)

    def wait_for_stopped(
            self, vm_name, timeout=DEFAULT_TIMEOUT_SHORT, namespace=DEFAULT_NAMESPACE):
        """Wait for VM to be stopped.

        Requires BOTH the VM object's status.printableStatus == "Stopped"
        (what the Harvester UI shows) AND the VMI object to be gone.
        printableStatus turns "Stopped" while the old VMI can still exist in
        phase Succeeded; starting the VM in that window deadlocks under
        RerunOnFailure (a Succeeded VMI is never restarted, and leaving
        Halted also stops its cleanup), so "stopped" here means teardown has
        fully finished.
        """
        endtime = time.time() + timeout
        last_log_time = 0

        while time.time() < endtime:
            try:
                vm = self.get(vm_name, namespace)
                printable = vm.get('status', {}).get('printableStatus', 'Unknown')
                if printable == 'Stopped' and self._vmi_is_gone(vm_name, namespace):
                    logging(f"VM {namespace}/{vm_name} is stopped")
                    return True
            except ApiException as e:
                printable = f"<error {e.status}>"
                logging(f"Error checking VM stopped status: {e}")

            now = time.time()
            if now - last_log_time >= 30:
                logging(f"Waiting for VM {vm_name} to stop "
                        f"(printableStatus: {printable})...")
                last_log_time = now

            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {namespace}/{vm_name} did not stop within {timeout}s"
        )

    def wait_for_deleted(
            self, vm_name, timeout=DEFAULT_TIMEOUT_SHORT, namespace=DEFAULT_NAMESPACE):
        """Wait for VM to be deleted."""
        wait_for_cr_deleted(
            group=KUBEVIRT_API_GROUP,
            version=KUBEVIRT_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINE_PLURAL,
            name=vm_name,
            timeout=timeout
        )

    def get_status(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Get VM status."""
        vm = self.get(vm_name, namespace)

        return {
            'runStrategy': vm.get('spec', {}).get('runStrategy', 'Unknown'),
            'conditions': vm.get('status', {}).get('conditions', [])
        }

    def wait_for_ip_addresses(
            self, vm_name, networks=None, timeout=DEFAULT_TIMEOUT_SHORT,
            namespace=DEFAULT_NAMESPACE):
        """Wait for VM to get IP addresses."""
        if networks is None:
            networks = ['default']
        elif isinstance(networks, str):
            # Handle case where Robot Framework passes string like "['default']"
            import ast
            try:
                networks = ast.literal_eval(networks)
            except (ValueError, SyntaxError):
                # If it's just a plain string, treat it as a single network
                networks = [networks]

        logging(
            f"Waiting for VM {namespace}/{vm_name} to get IP addresses "
            f"on networks: {networks}"
        )

        endtime = time.time() + timeout

        while time.time() < endtime:
            try:
                vmi = self.obj_api.get_namespaced_custom_object(
                    group=KUBEVIRT_API_GROUP,
                    version=KUBEVIRT_API_VERSION,
                    namespace=namespace,
                    plural=VIRTUALMACHINEINSTANCE_PLURAL,
                    name=vm_name
                )

                # status.interfaces can be explicitly null (not just
                # missing) before the VM's networking is fully up, and
                # dict.get(key, default) only applies the default when the
                # key is absent -- not when its value is None -- so `or []`
                # is needed to avoid a TypeError iterating over None.
                interfaces = vmi.get('status', {}).get('interfaces') or []

                # Check each requested network for IP address
                got_all_ips = True
                for network in networks:
                    has_ip = any(
                        iface['name'] == network and iface.get('ipAddress')
                        for iface in interfaces
                    )
                    if not has_ip:
                        got_all_ips = False
                        logging(
                            f"Network '{network}' does not have IP yet"
                        )
                        break

                if got_all_ips:
                    # Log the IP addresses found
                    ips = {
                        iface['name']: iface.get('ipAddress')
                        for iface in interfaces
                        if iface['name'] in networks
                    }
                    logging(
                        f"VM {namespace}/{vm_name} got IP addresses: {ips}"
                    )
                    return True

                logging(
                    f"Waiting for VM to get IP addresses "
                    f"(networks: {networks}, {len(interfaces)} interfaces)..."
                )

            except ApiException as e:
                logging(f"Error checking VM IP: {e}")

            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {namespace}/{vm_name} did not get IP addresses "
            f"within {timeout}s"
        )

    def _vm_subresource(self, vm_name, action, namespace=DEFAULT_NAMESPACE, body=None):
        """Call a kubevirt VirtualMachine subresource (e.g. addvolume/removevolume)
        via the aggregated subresources.kubevirt.io API.
        """
        path = (f"/apis/subresources.{KUBEVIRT_API_GROUP}/{KUBEVIRT_API_VERSION}"
                f"/namespaces/{namespace}/{VIRTUALMACHINE_PLURAL}"
                f"/{vm_name}/{action}")
        return self.obj_api.api_client.call_api(
            path, "PUT",
            header_params={"Content-Type": "application/json"},
            body=body if body is not None else {},
            auth_settings=["BearerToken"],
            response_type=None,
            _return_http_data_only=True,
            _preload_content=False,
        )

    def add_volume(self, vm_name, disk_name, volume_name,
                   namespace=DEFAULT_NAMESPACE, bus="scsi"):
        """Hot-plug an existing PVC into a running VM (kubevirt addvolume).

        Hot-plugged disks use the scsi bus.
        """
        body = {
            "name": disk_name,
            "disk": {"name": disk_name, "disk": {"bus": bus}},
            "volumeSource": {
                "persistentVolumeClaim": {"claimName": volume_name}
            },
        }
        logging(f"Hot-plugging volume {volume_name} as {disk_name} into "
                f"VM {namespace}/{vm_name}")
        self._vm_subresource(vm_name, "addvolume", namespace, body)

    def remove_volume(self, vm_name, disk_name, namespace=DEFAULT_NAMESPACE):
        """Hot-unplug a disk from a running VM (kubevirt removevolume)."""
        logging(f"Hot-unplugging disk {disk_name} from VM {namespace}/{vm_name}")
        self._vm_subresource(vm_name, "removevolume", namespace, {"name": disk_name})

    def get_volume_status(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return the VMI status.volumeStatus list (attach/hotplug state)."""
        vmi = self.obj_api.get_namespaced_custom_object(
            group=KUBEVIRT_API_GROUP,
            version=KUBEVIRT_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEINSTANCE_PLURAL,
            name=vm_name
        )
        return vmi.get('status', {}).get('volumeStatus', [])

    def wait_for_volume_hotplugged(self, vm_name, disk_name,
                                   timeout=DEFAULT_TIMEOUT_SHORT,
                                   namespace=DEFAULT_NAMESPACE):
        """Wait for a hot-plugged disk to reach the Ready phase in volumeStatus."""
        endtime = time.time() + timeout
        last_log_time = 0
        while time.time() < endtime:
            try:
                for vs in self.get_volume_status(vm_name, namespace):
                    if vs.get('name') == disk_name:
                        phase = vs.get('phase', 'Unknown')
                        if phase == 'Ready':
                            logging(f"Volume {disk_name} hot-plugged into "
                                    f"{vm_name} (phase={phase})")
                            return True
                        now = time.time()
                        if now - last_log_time >= 30:
                            logging(f"Waiting for {disk_name} hotplug "
                                    f"(phase={phase})...")
                            last_log_time = now
            except ApiException as e:
                logging(f"Error checking volumeStatus: {e}")
            time.sleep(self.retry_interval)
        raise AssertionError(
            f"Volume {disk_name} was not hot-plugged into {vm_name} "
            f"within {timeout}s")

    def wait_for_volume_unplugged(self, vm_name, disk_name,
                                  timeout=DEFAULT_TIMEOUT_SHORT,
                                  namespace=DEFAULT_NAMESPACE):
        """Wait for a disk to disappear from the VMI volumeStatus."""
        endtime = time.time() + timeout
        while time.time() < endtime:
            try:
                names = [vs.get('name')
                         for vs in self.get_volume_status(vm_name, namespace)]
                if disk_name not in names:
                    logging(f"Volume {disk_name} hot-unplugged from {vm_name}")
                    return True
            except ApiException as e:
                logging(f"Error checking volumeStatus: {e}")
            time.sleep(self.retry_interval)
        raise AssertionError(
            f"Volume {disk_name} was not hot-unplugged from {vm_name} "
            f"within {timeout}s")

    def get_disk_names(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return the disk names declared in the VM spec (boot + data disks)."""
        vm = self.get(vm_name, namespace)
        disks = (vm.get('spec', {}).get('template', {}).get('spec', {})
                 .get('domain', {}).get('devices', {}).get('disks', []))
        return [d.get('name') for d in disks]

    def cleanup(self):
        """Clean up test VMs."""
        logging('Cleaning up test VMs')

        try:
            vms = self.list(
                namespace=DEFAULT_NAMESPACE,
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )

            for vm in vms:
                vm_name = vm['metadata']['name']
                namespace = vm['metadata']['namespace']

                try:
                    logging(f'Deleting test VM: {namespace}/{vm_name}')
                    self.delete(vm_name, namespace)
                except Exception as e:
                    logging(f'Error deleting VM {vm_name}: {e}', 'WARNING')
        except Exception as e:
            logging(f'Error during VM cleanup: {e}', 'WARNING')

    def _get_disk_template(self, vm_name, disk_name, namespace):
        vm = self.get(vm_name, namespace)
        annotations = vm.get("metadata", {}).get("annotations", {})
        templates_raw = annotations.get("harvesterhci.io/volumeClaimTemplates")
        if not templates_raw:
            raise Exception(f"VM {vm_name} has no volumeClaimTemplates annotation")

        try:
            volume_claim_templates = json.loads(templates_raw)
        except json.JSONDecodeError as e:
            raise Exception(f"Invalid volumeClaimTemplates JSON for VM {vm_name}: {e}")

        for template in volume_claim_templates:
            if template.get("metadata", {}).get("name") == disk_name:
                return vm, annotations, volume_claim_templates, template
        raise Exception(f"VM {vm_name} has no disk with PVC name {disk_name}")

    def update_disk_size(self, vm_name, disk_name, new_size, namespace=DEFAULT_NAMESPACE):
        """Update VM disk size via volumeClaimTemplates annotation."""
        vm, annotations, volume_claim_templates, target = self._get_disk_template(
            vm_name, disk_name, namespace
        )

        # Modify spec with new configuration
        target.setdefault(
            "spec", {}).setdefault(
                "resources", {}).setdefault(
                    "requests", {})["storage"] = new_size
        annotations["harvesterhci.io/volumeClaimTemplates"] = json.dumps(volume_claim_templates)
        vm.setdefault("metadata", {})["annotations"] = annotations

        # Apply updated spec
        replace_cr(
            group=KUBEVIRT_API_GROUP,
            version=KUBEVIRT_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINE_PLURAL,
            name=vm_name,
            body=vm
        )
        logging(f"Updated disk size for {vm_name} {disk_name} to {new_size}")

    def get_cpu_cores(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return the VM spec's requested CPU core count."""
        vm = self.get(vm_name, namespace)
        return vm['spec']['template']['spec']['domain']['cpu']['cores']

    def update_cpu_cores(self, vm_name, cpu_cores, namespace=DEFAULT_NAMESPACE,
                         retry_count=5):
        """Update a VM's CPU core count (spec.template.spec.domain.cpu.cores),
        keeping resources.limits.cpu in sync. Retries on 409 Conflict (object
        modified concurrently between the get and the replace) by re-fetching
        and reapplying the change.
        """
        for attempt in range(retry_count):
            vm = self.get(vm_name, namespace)
            domain = vm['spec']['template']['spec']['domain']
            domain.setdefault('cpu', {})['cores'] = int(cpu_cores)
            domain.setdefault('resources', {}).setdefault(
                'limits', {})['cpu'] = str(cpu_cores)

            try:
                replace_cr(
                    group=KUBEVIRT_API_GROUP,
                    version=KUBEVIRT_API_VERSION,
                    namespace=namespace,
                    plural=VIRTUALMACHINE_PLURAL,
                    name=vm_name,
                    body=vm
                )
                logging(f"Updated VM {namespace}/{vm_name} CPU cores to {cpu_cores}")
                return
            except ApiException as e:
                if e.status == 409 and attempt < retry_count - 1:
                    logging(f"Conflict updating VM {vm_name} CPU cores, "
                            f"retrying ({attempt + 1}/{retry_count})...")
                    time.sleep(1)
                    continue
                raise
