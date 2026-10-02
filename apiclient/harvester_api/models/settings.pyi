from __future__ import annotations
from typing import NoReturn, Optional, Type
from typing_extensions import override


class BaseSettingSpec:
    """
    """

    data: dict

    def __init__(self, data: dict) -> NoReturn:
        """
        """
    def to_dict(self) -> dict:
        """
        """
    @classmethod
    def from_dict(cls: Type[BaseSettingSpec], data: dict) -> BaseSettingSpec:
        """
        """


class BackupTargetSpec(BaseSettingSpec):
    """
    """

    value: Optional[dict]

    def __init__(self, value: Optional[dict] = ...) -> NoReturn:
        """
        """
    @property
    def type(self) -> str | None:
        """
        """
    def clear(self) -> NoReturn:
        """
        """
    def to_dict(self) -> dict:
        """
        """
    @override
    @classmethod
    def from_dict(cls: Type[BackupTargetSpec], data: dict) -> BackupTargetSpec:
        """
        """
    @classmethod
    def S3(
        cls: Type[BackupTargetSpec],
        bucket: str,
        region: str,
        access_id: str,
        access_secret,
        endpoint: str = ...,
        virtual_hosted: Optional[bool] = ...
    ) -> Type[BackupTargetSpec]:
        """
        """
    @classmethod
    def NFS(cls: Type[BackupTargetSpec], endpoint: str) -> Type[BackupTargetSpec]:
        """
        """

class KubeconfigDefaultTokenTTLSpec(BaseSettingSpec):
    """Kubeconfig Default Token TTL Spec setting

    Args:
        BaseSettingSpec (_type_): _description_
    """
    value: Optional[dict]

    def __init__(self, value: Optional[dict] = ...) -> NoReturn:
        """
        """
    @property
    def type(self) -> str | None:
        """
        """
    def clear(self) -> NoReturn:
        """
        """
    def to_dict(self) -> dict:
        """
        """

    @classmethod
    def TTL(cls: Type[KubeconfigDefaultTokenTTLSpec], minutes: int) -> Type[KubeconfigDefaultTokenTTLSpec]:
        """Class method builds token ttl

        Args:
            cls (Type[KubeconfigDefaultTokenTTLSpec]): spec of kubeconfig default token ttl
            minutes (int): minutes must be integer

        Returns:
            Type[KubeconfigDefaultTokenTTLSpec]: base spec to return with single value int of minutes on token ttl
        """