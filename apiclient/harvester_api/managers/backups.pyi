from typing import ClassVar, Optional, Type
from typing_extensions import override

from requests.models import Response

from harvester_api.models.backups import RestoreSpec as RSpec, SnapshotRestoreSpec
from .base import BaseManager


class BackupManager(BaseManager):
    BACKUP_fmt: ClassVar[str]
    RESTORE_fmt: ClassVar[str]
    RestoreSpec: ClassVar[Type[RSpec]]

    def get(
        self,
        name: str = ...,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
    def create(
        self,
        *args,
        **kwargs
    ) -> dict | Response:
        """
        """
    def delete(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
    def update(
        self,
        name: str,
        backup_spec: str | dict,
        namespace: str = ...,
        *,
        raw: bool = ...,
        as_json: bool = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
    def restore(
        self,
        name: str,
        restore_spec: Type[RSpec],
        namespace: str = ...,
        *,
        raw: bool = ...,
        **kwargs
    ) -> dict | Response:
        """
        """


class VirtualMachineSnapshotManager(BackupManager):
    RestoreSpec: ClassVar[Type[SnapshotRestoreSpec]]

    def create_data(
        self,
        vm_uid: str,
        vm_name: str,
        snapshot_name: str,
        namespace: str
    ) -> dict:
        """
        """
    def get(
        self,
        name: str = ...,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        **kwargs
    ) -> dict | Response:
        """
        """

    @override
    def create(
        self,
        vm_name: str,
        snapshot_name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
