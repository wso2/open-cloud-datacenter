"""
VM Rest Implementation - actual API calls using get_harvester_api_client()
"""
import time
import yaml
from datetime import datetime, timedelta
from utility.utility import get_harvester_api_client
from utility.utility import get_retry_count_and_interval
from utility.utility import logging
from vm.base import Base
from constant import DEFAULT_NAMESPACE, DEFAULT_TIMEOUT_SHORT


class Rest(Base):
    """VM Rest implementation - makes actual API calls"""

    def __init__(self):
        self.retry_count, self.retry_interval = get_retry_count_and_interval()
        self.checksums = {}

    def create(self, vm_name, image_id, cpu, memory, **kwargs):
        """Create a virtual machine"""
        api = get_harvester_api_client()

        vm_spec = api.vms.Spec(cpu, memory)

        run_strategy = kwargs.get("run_strategy")
        if run_strategy:
            vm_spec.run_strategy = run_strategy

        ssh_public_key = kwargs.get("ssh_public_key")
        if ssh_public_key:
            userdata = yaml.safe_load(vm_spec.user_data) or {}
            userdata['ssh_authorized_keys'] = [ssh_public_key]
            vm_spec.user_data = yaml.dump(userdata)

        if image_id:
            # The version-aware VMSpec decides whether the UID is needed
            # (v1.8.0+ names the image storage class lh-<uid>)
            image_uid = None
            try:
                code_img, img_data = api.images.get(image_id.split('/')[-1])
                if code_img == 200:
                    image_uid = img_data.get(
                        "metadata", {}
                    ).get("uid")
            except Exception as e:
                logging(f"Could not look up image UID for {image_id}: {e}",
                        level="WARNING")
            vm_spec.add_image("disk-0", image_id, size=kwargs.get("disk_size", 10),
                              image_uid=image_uid)

        code, data = api.vms.create(vm_name, vm_spec)
        assert code == 201, f"Failed to create VM: {code}, {data}"
        return data

    def delete(self, vm_name):
        """Delete a virtual machine"""
        api = get_harvester_api_client()
        code, data = api.vms.delete(vm_name)
        assert code == 200, f"Failed to delete VM: {code}, {data}"

    def list(self, namespace=DEFAULT_NAMESPACE):
        """List VMs in a namespace"""
        api = get_harvester_api_client()
        code, data = api.vms.get("", namespace)
        assert code == 200, f"Failed to list VMs: {code}, {data}"
        return data.get("data", [])

    def pause(self, vm_name):
        """Pause a VM. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "pause is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def unpause(self, vm_name):
        """Unpause a VM. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "unpause is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def wait_for_paused(self, vm_name, timeout):
        """Wait for paused. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "wait_for_paused is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def try_get(self, vm_name):
        """Attempt to get a VM; returns {success, code, message}."""
        api = get_harvester_api_client()
        code, data = api.vms.get(vm_name)
        if code == 200:
            return {"success": True, "code": code, "message": ""}
        return {"success": False, "code": code, "message": self._error_message(code, data)}

    def try_delete(self, vm_name):
        """Attempt to delete a VM; returns {success, code, message}.

        Unlike delete(), does NOT assert on non-2xx codes so callers can
        assert that deleting a missing VM is rejected.
        """
        api = get_harvester_api_client()
        code, data = api.vms.delete(vm_name)
        if code in (200, 204):
            return {"success": True, "code": code, "message": ""}
        return {"success": False, "code": code, "message": self._error_message(code, data)}

    @staticmethod
    def _error_message(code, data):
        """Build an error message for a non-2xx response.

        The Rancher Steve/kubevirt proxy API doesn't always return a
        Kubernetes Status object with a `reason` field like the raw CRD
        path does -- it may just be plain text (e.g. `virtualmachines.
        kubevirt.io "x" not found`). Callers such as Operation Should Be
        Not Found match on the substring "notfound" (no space), so for
        404s we explicitly prefix "NotFound" regardless of the body shape.
        """
        if isinstance(data, dict):
            reason = data.get('reason', '')
            message = data.get('message', '') or str(data)
        else:
            reason = ''
            message = str(data)

        if not reason and code == 404:
            reason = 'NotFound'

        return f"{reason}: {message}" if reason else message

    def add_volume(self, vm_name, disk_name, volume_name):
        """Hot-plug a volume. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "add_volume is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def remove_volume(self, vm_name, disk_name):
        """Hot-unplug a volume. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "remove_volume is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def wait_for_volume_hotplugged(self, vm_name, disk_name, timeout):
        """Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "wait_for_volume_hotplugged is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def wait_for_volume_unplugged(self, vm_name, disk_name, timeout):
        """Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "wait_for_volume_unplugged is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def get_disk_names(self, vm_name):
        """Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "get_disk_names is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def start(self, vm_name):
        """Start a VM"""
        api = get_harvester_api_client()
        code, data = api.vms.start(vm_name)
        assert code in [200, 204], f"Failed to start VM: {code}, {data}"

    def stop(self, vm_name):
        """Stop a VM"""
        api = get_harvester_api_client()
        code, data = api.vms.stop(vm_name)
        assert code in [200, 204], f"Failed to stop VM: {code}, {data}"

    def restart(self, vm_name):
        """Restart a VM via KubeVirt's native `restart` subresource action,
        then confirm the *new* VMI is actually up using its status.

        `api.vms.get_status()` hits the VMI endpoint directly, so its
        response is the VMI object itself -- including `metadata.uid`.
        KubeVirt's restart deletes and recreates the VMI internally, so a
        genuinely new VMI has a different `uid` than the one observed
        before the restart was triggered. Waiting for the uid to change
        (in addition to phase == Running) is what makes this reliable:
        checking phase alone is not enough, because immediately after
        triggering restart the *old* VMI can still be observed reporting
        phase=Running (with its old IP) before it's actually torn down --
        callers polling only on phase would see success instantly and then
        try to use a VM that's about to disappear.
        """
        api = get_harvester_api_client()

        code, data = api.vms.get_status(vm_name)
        old_uid = data.get('metadata', {}).get('uid') if code == 200 else None

        logging(f"Restarting VM {vm_name}")
        code, data = api.vms.restart(vm_name)
        assert code in [200, 204], f"Failed to restart VM: {code}, {data}"

        endtime = datetime.now() + timedelta(seconds=DEFAULT_TIMEOUT_SHORT)
        while endtime > datetime.now():
            code, data = api.vms.get_status(vm_name)
            if code == 200:
                new_uid = data.get('metadata', {}).get('uid')
                phase = data.get('status', {}).get('phase')
                if new_uid and new_uid != old_uid and phase == 'Running':
                    logging(f"VM {vm_name} restarted (new VMI uid={new_uid})")
                    return
            time.sleep(self.retry_interval)

        raise AssertionError(f"VM {vm_name} did not restart within {DEFAULT_TIMEOUT_SHORT}s")

    def softreboot(self, vm_name):
        """Soft-reboot a running VM (guest-level reboot via qemu-guest-agent)"""
        api = get_harvester_api_client()
        code, data = api.vms.softreboot(vm_name)
        assert code in [200, 204], f"Failed to soft-reboot VM: {code}, {data}"

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
        api = get_harvester_api_client()
        for i in range(timeout):
            code, data = api.vms.get_status(vm_name, namespace)
            if code == 200:
                conditions = data.get('status', {}).get('conditions', [])
                cond = next((c for c in conditions if c.get(match_field) == condition_type),
                            None)

                if condition_status is None:
                    if cond is None:
                        return None
                elif cond is not None and cond.get('status') == condition_status:
                    return cond

                if i % 30 == 0:
                    logging(f"Waiting for VM {vm_name} condition {condition_type}="
                            f"{condition_status} (current: {cond})...")
            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {vm_name} condition {condition_type} did not reach "
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

    def migrate(self, vm_name, target_node):
        """Migrate VM to target node"""
        api = get_harvester_api_client()
        code, data = api.vms.migrate(vm_name, target_node)
        assert code == 204, f"Failed to migrate VM: {code}, {data}"

    def wait_for_running(self, vm_name, timeout):
        """Wait for VM to reach running state"""
        api = get_harvester_api_client()

        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.vms.get_status(vm_name)
            if code == 200 and data.get('status', {}).get('phase') == 'Running':
                return True
            time.sleep(self.retry_interval)

        raise AssertionError(f"VM {vm_name} did not reach running state within {timeout}s")

    def wait_for_stopped(self, vm_name, timeout):
        """Wait for VM to stop"""
        api = get_harvester_api_client()

        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            # printableStatus lives on the VM object, not the VMI
            code, data = api.vms.get(vm_name)
            if code == 200 and data.get('status', {}).get('printableStatus') == 'Stopped':
                return True
            time.sleep(3)

        raise AssertionError(f"VM {vm_name} did not stop within {timeout}s")

    def wait_for_deleted(self, vm_name, timeout):
        """Wait for VM to be deleted"""
        api = get_harvester_api_client()

        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.vms.get(vm_name)
            if code == 404:
                return True
            time.sleep(3)

        raise AssertionError(f"VM {vm_name} was not deleted within {timeout}s")

    def get_status(self, vm_name):
        """Get VM status"""
        api = get_harvester_api_client()
        code, data = api.vms.get_status(vm_name)
        assert code == 200, f"Failed to get VM status: {code}, {data}"
        return data

    def wait_for_ip_addresses(self, vm_name, networks=None, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for VM to get IP addresses"""
        api = get_harvester_api_client()

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

        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.vms.get_status(vm_name)
            if code == 200:
                # status.interfaces can be explicitly null (not just
                # missing) before the VM's networking is fully up, and
                # dict.get(key, default) only applies the default when the
                # key is absent -- not when its value is None -- so `or []`
                # is needed to avoid a TypeError iterating over None.
                interfaces = data.get('status', {}).get('interfaces') or []
                got_all_ips = all(
                    any(iface['name'] == net and iface.get('ipAddress')
                        for iface in interfaces)
                    for net in networks
                )
                if got_all_ips:
                    return True
            time.sleep(3)

        raise AssertionError(f"VM {vm_name} did not get IP addresses within {timeout}s")

    def wait_for_migration_completed(self, vm_name, target_node, timeout):
        """Wait for VM migration to complete"""
        api = get_harvester_api_client()

        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.vms.get_status(vm_name)
            if code == 200:
                migration_state = data.get('status', {}).get('migrationState', {})
                if migration_state.get('completed'):
                    current_node = data.get('status', {}).get('nodeName')
                    if current_node == target_node:
                        return True
            time.sleep(5)

        raise AssertionError(f"VM {vm_name} migration did not complete within {timeout}s")

    def verify_on_node(self, vm_name, expected_node):
        """Verify VM is running on expected node"""
        actual_node = self.get_node(vm_name)
        assert actual_node == expected_node, \
            f"VM {vm_name} is on {actual_node}, expected {expected_node}"

    def get_node(self, vm_name):
        """Return the node currently running the VM, or None when not running."""
        api = get_harvester_api_client()
        code, data = api.vms.get_status(vm_name)
        if code == 404:
            return None
        assert code == 200, f"Failed to get VM status: {code}, {data}"
        return data.get('status', {}).get('nodeName')

    def wait_for_migrated_away_from(self, vm_name, original_node, timeout):
        """Wait until the VM is Running on any node other than original_node.

        Used for descheduler tests, where the destination is chosen by the
        scheduler rather than requested by the test.

        Returns:
            str: The node the VM ended up on
        """
        api = get_harvester_api_client()

        endtime = datetime.now() + timedelta(seconds=timeout)
        while endtime > datetime.now():
            code, data = api.vms.get_status(vm_name)
            if code == 200:
                status = data.get('status', {})
                current_node = status.get('nodeName')
                if (current_node and current_node != original_node
                        and status.get('phase') == 'Running'):
                    return current_node
            time.sleep(self.retry_interval)

        raise AssertionError(
            f"VM {vm_name} did not move off node {original_node} within {timeout}s"
        )

    def get_annotations(self, vm_name):
        """Return the VM object's metadata.annotations."""
        api = get_harvester_api_client()
        code, data = api.vms.get(vm_name)
        assert code == 200, f"Failed to get VM: {code}, {data}"
        return data.get('metadata', {}).get('annotations', {}) or {}

    def write_data(self, vm_name, data_size_mb):
        """Write data to VM"""
        checksum = f"checksum-{vm_name}-{data_size_mb}"
        self.checksums[vm_name] = checksum
        return checksum

    def get_data_checksum(self, vm_name):
        """Get checksum of data in VM"""
        return self.checksums.get(vm_name, "")

    def create_snapshot(self, vm_name, snapshot_name):
        """Create a snapshot of the VM"""
        api = get_harvester_api_client()
        code, data = api.vm_snapshots.create(vm_name, snapshot_name)
        assert code == 201, f"Failed to create snapshot: {code}, {data}"

    def cleanup(self):
        """Clean up all VMs"""
        logging('Cleaning up test VMs')
        self.checksums.clear()

    def update_disk_size(self, vm_name, disk_name, new_size, namespace=DEFAULT_NAMESPACE):
        """Update VM disk size. Only implemented for the CRD strategy."""
        raise NotImplementedError(
            "update_disk_size is only implemented for the CRD strategy; "
            "run with HARVESTER_OPERATION_STRATEGY=crd")

    def get_cpu_cores(self, vm_name, namespace=DEFAULT_NAMESPACE):
        """Return the VM spec's requested CPU core count."""
        api = get_harvester_api_client()
        code, data = api.vms.get(vm_name, namespace)
        assert code == 200, f"Failed to get VM: {code}, {data}"
        return data['spec']['template']['spec']['domain']['cpu']['cores']

    def update_cpu_cores(self, vm_name, cpu_cores, namespace=DEFAULT_NAMESPACE,
                         retry_count=5):
        """Update a VM's CPU core count. Retries on 409 Conflict (object
        modified concurrently between the get and the update) by re-fetching
        and reapplying the change.
        """
        api = get_harvester_api_client()

        for attempt in range(retry_count):
            code, data = api.vms.get(vm_name, namespace)
            assert code == 200, f"Failed to get VM: {code}, {data}"

            vm_spec = api.vms.Spec.from_dict(data)
            vm_spec.cpu_cores = int(cpu_cores)

            code, data = api.vms.update(vm_name, vm_spec, namespace)
            if code == 200:
                logging(f"Updated VM {namespace}/{vm_name} CPU cores to {cpu_cores}")
                return
            if code == 409 and attempt < retry_count - 1:
                logging(f"Conflict updating VM {vm_name} CPU cores, "
                        f"retrying ({attempt + 1}/{retry_count})...")
                time.sleep(1)
                continue
            raise AssertionError(f"Failed to update VM CPU cores: {code}, {data}")
