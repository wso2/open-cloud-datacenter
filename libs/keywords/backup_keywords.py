""" Backup Keyword Wrapper

Layer 3: Keyword wrapper (NO direct API calls)
"""

import json
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from backup import Backup  # noqa E402
from setting import Setting  # noqa E402
from constant import SETTING_BACKUP_TARGET, DEFAULT_TIMEOUT_LONG, DEFAULT_TIMEOUT_SHORT  # noqa E402
from utility.utility import logging  # noqa E402


class backup_keywords:
    """Backup keyword wrapper - creates Backup component and delegates"""

    def __init__(self):
        """Initialize backup keywords with lazy loading"""
        self._backup = None
        self._setting = None

    @property
    def backup(self):
        """Lazy initialize backup to allow API client setup first"""
        if self._backup is None:
            self._backup = Backup()
        return self._backup

    @property
    def setting(self):
        """Lazy initialize setting to allow API client setup first"""
        if self._setting is None:
            self._setting = Setting()
        return self._setting

    def get_backup_target(self):
        """Get the cluster backup-target setting as a dict.

        Returns {} when the setting has no value configured.
        """
        value = self.setting.get(SETTING_BACKUP_TARGET).get('value')
        if not value:
            return {}
        return json.loads(value)

    def set_nfs_backup_target(self, endpoint):
        """Configure the cluster backup-target setting to an NFS endpoint.

        Harvester's webhook validates connectivity on update, so a bad
        endpoint fails here instead of at backup time.
        """
        assert endpoint.startswith('nfs://'), (
            f"NFS backup-target endpoint should start with 'nfs://', got {endpoint}"
        )
        logging(f'Setting backup-target to NFS endpoint {endpoint}')
        value = json.dumps({"type": "nfs", "endpoint": endpoint})
        self.setting.update(SETTING_BACKUP_TARGET, value)

    def create_backup(self, vm_name, backup_name):
        """Create a backup of the VM"""
        logging(f'Creating backup {backup_name} for VM {vm_name}')
        self.backup.create(vm_name, backup_name)

    def wait_for_backup_ready(self, backup_name, timeout=DEFAULT_TIMEOUT_LONG):
        """Wait for backup to become ready to use"""
        logging(f'Waiting for backup {backup_name} to be ready')
        self.backup.wait_for_ready(backup_name, int(timeout))

    def delete_backup(self, backup_name):
        """Delete a backup"""
        logging(f'Deleting backup {backup_name}')
        self.backup.delete(backup_name)

    def wait_for_backup_deleted(self, backup_name, timeout=DEFAULT_TIMEOUT_SHORT):
        """Wait for backup to be removed"""
        logging(f'Waiting for backup {backup_name} to be deleted')
        self.backup.wait_for_deleted(backup_name, int(timeout))

    def restore_backup_to_new_vm(self, backup_name, restore_name, new_vm_name):
        """Restore a backup into a brand-new VM"""
        logging(f'Restoring backup {backup_name} to new VM {new_vm_name}')
        self.backup.restore_to_new_vm(backup_name, restore_name, new_vm_name)

    def restore_backup_replace_vm(self, backup_name, restore_name, delete_volumes=True):
        """Restore a backup over its (stopped) source VM"""
        logging(f'Restoring backup {backup_name} replacing its source VM')
        self.backup.restore_replace_vm(backup_name, restore_name, delete_volumes)

    def wait_for_restore_completed(self, restore_name, timeout=DEFAULT_TIMEOUT_LONG):
        """Wait for a restore to complete"""
        logging(f'Waiting for restore {restore_name} to complete')
        self.backup.wait_for_restore_completed(restore_name, int(timeout))

    def delete_restore(self, restore_name):
        """Delete a restore bookkeeping object"""
        logging(f'Deleting restore {restore_name}')
        self.backup.delete_restore(restore_name)

    def get_backup_volume_names(self, backup_name):
        """Longhorn volume names recorded in the backup (capture before deleting it)"""
        return self.backup.get_backup_volume_names(backup_name)

    def cleanup_longhorn_backup_artifacts(self, volume_names, image_name):
        """Delete leftover longhorn BackupVolume/BackupBackingImage CRs for
        this suite's volumes and image"""
        logging(f'Cleaning longhorn backup artifacts (volumes={volume_names}, '
                f'image={image_name})')
        self.backup.cleanup_longhorn_backup_artifacts(volume_names, image_name)
