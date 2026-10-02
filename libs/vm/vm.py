
"""
VM Component - delegates to CRD or REST implementation
"""
import os

from constant import HarvesterOperationStrategy, DEFAULT_NAMESPACE
from vm.rest import Rest
from vm.crd import CRD
from vm.base import Base


class VM(Base):
    """VM component - selects implementation by HARVESTER_OPERATION_STRATEGY"""
    def __init__(self):
        strategy_str = os.getenv("HARVESTER_OPERATION_STRATEGY", "crd").lower()
        if strategy_str == HarvesterOperationStrategy.REST.value:
            self.vm = Rest()
        else:
            self.vm = CRD()

    def create(self, vm_name, image_id, cpu, memory, **kwargs):
        return self.vm.create(vm_name, image_id, cpu, memory, **kwargs)

    def delete(self, vm_name):
        return self.vm.delete(vm_name)

    def list(self, namespace=DEFAULT_NAMESPACE):
        return self.vm.list(namespace)

    def start(self, vm_name):
        return self.vm.start(vm_name)

    def stop(self, vm_name):
        return self.vm.stop(vm_name)

    def restart(self, vm_name):
        return self.vm.restart(vm_name)

    def softreboot(self, vm_name):
        return self.vm.softreboot(vm_name)

    def wait_vm_condition(self, vm_name, condition_type, condition_status, timeout,
                          match_field="type"):
        return self.vm.wait_vm_condition(vm_name, condition_type, condition_status, timeout,
                                         match_field=match_field)

    def wait_for_agent_connected(self, vm_name, timeout):
        return self.vm.wait_for_agent_connected(vm_name, timeout)

    def wait_for_agent_disconnected(self, vm_name, timeout):
        return self.vm.wait_for_agent_disconnected(vm_name, timeout)

    def pause(self, vm_name):
        return self.vm.pause(vm_name)

    def unpause(self, vm_name):
        return self.vm.unpause(vm_name)

    def wait_for_paused(self, vm_name, timeout):
        return self.vm.wait_for_paused(vm_name, timeout)

    def try_get(self, vm_name):
        return self.vm.try_get(vm_name)

    def try_delete(self, vm_name):
        return self.vm.try_delete(vm_name)

    def add_volume(self, vm_name, disk_name, volume_name):
        return self.vm.add_volume(vm_name, disk_name, volume_name)

    def remove_volume(self, vm_name, disk_name):
        return self.vm.remove_volume(vm_name, disk_name)

    def wait_for_volume_hotplugged(self, vm_name, disk_name, timeout):
        return self.vm.wait_for_volume_hotplugged(vm_name, disk_name, timeout)

    def wait_for_volume_unplugged(self, vm_name, disk_name, timeout):
        return self.vm.wait_for_volume_unplugged(vm_name, disk_name, timeout)

    def get_disk_names(self, vm_name):
        return self.vm.get_disk_names(vm_name)

    def migrate(self, vm_name, target_node):
        return self.vm.migrate(vm_name, target_node)

    def wait_for_running(self, vm_name, timeout):
        return self.vm.wait_for_running(vm_name, timeout)

    def wait_for_stopped(self, vm_name, timeout):
        return self.vm.wait_for_stopped(vm_name, timeout)

    def wait_for_deleted(self, vm_name, timeout):
        return self.vm.wait_for_deleted(vm_name, timeout)

    def get_status(self, vm_name):
        return self.vm.get_status(vm_name)

    def wait_for_ip_addresses(self, vm_name, networks, timeout):
        return self.vm.wait_for_ip_addresses(vm_name, networks, timeout)

    def wait_for_migration_completed(self, vm_name, target_node, timeout):
        return self.vm.wait_for_migration_completed(vm_name, target_node, timeout)

    def wait_for_migrated_away_from(self, vm_name, original_node, timeout):
        return self.vm.wait_for_migrated_away_from(vm_name, original_node, timeout)

    def verify_on_node(self, vm_name, expected_node):
        return self.vm.verify_on_node(vm_name, expected_node)

    def get_node(self, vm_name):
        return self.vm.get_node(vm_name)

    def get_annotations(self, vm_name):
        return self.vm.get_annotations(vm_name)

    def write_data(self, vm_name, data_size_mb):
        return self.vm.write_data(vm_name, data_size_mb)

    def get_data_checksum(self, vm_name):
        return self.vm.get_data_checksum(vm_name)

    def create_snapshot(self, vm_name, snapshot_name):
        return self.vm.create_snapshot(vm_name, snapshot_name)

    def cleanup(self):
        return self.vm.cleanup()

    def update_disk_size(self, vm_name, disk_name, new_size, namespace=DEFAULT_NAMESPACE):
        return self.vm.update_disk_size(vm_name, disk_name, new_size, namespace)

    def get_cpu_cores(self, vm_name, namespace=DEFAULT_NAMESPACE):
        return self.vm.get_cpu_cores(vm_name, namespace)

    def update_cpu_cores(self, vm_name, cpu_cores, namespace=DEFAULT_NAMESPACE):
        return self.vm.update_cpu_cores(vm_name, cpu_cores, namespace)
