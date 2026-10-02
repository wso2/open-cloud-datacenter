"""
Snapshot Keywords - creates Snapshot() instance and delegates - NO direct API calls!
"""

import os
import sys

# add utility module to import paths
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "../"))) # noqa E402

from utility.utility import logging # noqa E402
from snapshot import Snapshot # noqa E402


class snapshot_keywords:
    """
    Snapshot Keyword Wrapper - Creates Snapshot component and delegates
    """

    def __init__(self):
        self.snap = Snapshot()

    def create_snapshot(self, vm_name, snapshot_name, **kwargs):
        """Create a new snapshot from VM"""
        logging(f"Creating snapshot {snapshot_name} from VM {vm_name}")
        self.snap.create(vm_name, snapshot_name, **kwargs)

    def delete_snapshot(self, snapshot_name, **kwargs):
        """Delete a snapshot"""
        logging(f"Deleting snapshot {snapshot_name}")
        self.snap.delete(snapshot_name, **kwargs)

    def wait_snapshot_ready(self, snapshot_name, **kwargs):
        logging(f"Waiting for snapshot {snapshot_name} to be ready")
        self.snap.wait_ready(snapshot_name, **kwargs)

    def wait_snapshot_deleted(self, snapshot_name, **kwargs):
        logging(f"Waiting for snapshot {snapshot_name} to be deleted")
        self.snap.wait_deleted(snapshot_name, **kwargs)
