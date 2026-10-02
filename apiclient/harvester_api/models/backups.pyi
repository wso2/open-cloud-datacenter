from __future__ import annotations
from typing import Optional, NoReturn, Type


class _BaseBackup:
    """
    """
    new_vm: bool
    vm_name: str
    namespace: str
    delete_volumes: bool

    def __init__(
        self,
        new_vm: bool,
        vm_name: Optional[str] = ...,
        namespace: Optional[str] = ...,
        delete_volumes: Optional[bool] = ...
    ) -> NoReturn:
        """
        """
    def to_dict(
        self,
        name: str,
        namespace: str,
        existing_vm: str
    ) -> dict:
        """
        """


class RestoreSpec(_BaseBackup):
    """
    """
    @classmethod
    def for_new(
        cls,
        vm_name: str,
        namespace: Optional[str] = ...
    ) -> Type[RestoreSpec]:
        """
        """
    @classmethod
    def for_existing(
        cls,
        delete_volumes: Optional[bool] = ...
    ) -> Type[RestoreSpec]:
        """
        """


class SnapshotRestoreSpec(_BaseBackup):
    @classmethod
    def for_new(
        cls,
        vm_name: str,
    ) -> Type[SnapshotRestoreSpec]:
        """
        """
    @classmethod
    def for_existing(
        cls,
    ) -> Type[SnapshotRestoreSpec]:
        """
        """
