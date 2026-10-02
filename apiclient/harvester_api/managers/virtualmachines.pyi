from typing import ClassVar, Optional, Type

from harvester_api.models.virtualmachines import VMSpec
from .base import BaseManager


class VirtualMachineManager(BaseManager):
    API_VERSION: ClassVar[str]
    PATH_fmt: ClassVar[str]
    VMI_fmt: ClassVar[str]
    VMIOP_fmt: ClassVar[str]
    Spec: Type[VMSpec]

    def get(
        self,
        name: str = ...,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        **kwargs
    ):
        """
        """
    def create(
        self,
        name: str,
        vm_spec: Type[VMSpec],
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def update(
        self,
        name: str,
        vm_spec: Type[VMSpec],
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        as_json: Optional[bool] = ...,
        **kwargs
    ):
        """
        """
    def delete(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def clone(
        self,
        name: str,
        new_vm_name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def backup(
        self,
        name: str,
        backup_name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def start(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def restart(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def stop(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def migrate(
        self,
        name: str,
        target_node: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def abort_migrate(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def pause(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def unpause(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
    def softreboot(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ):
        """
        """
