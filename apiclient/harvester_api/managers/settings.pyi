from typing import ClassVar, Optional, Type
from collections.abc import Mapping

from requests.models import Response

from harvester_api.models.settings import BaseSettingSpec, BackupTargetSpec
from .base import BaseManager


class SettingManager(BaseManager):
    vlan: ClassVar[str]
    PATH_fmt: ClassVar[str]
    Spec: Type[BaseSettingSpec]
    BackupTargetSpec: Type[BackupTargetSpec]

    def get(
        self,
        name: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def update(
        self,
        name: str,
        spec: Type[BaseSettingSpec] | Mapping,
        *,
        raw: Optional[bool] = ...,
        as_json: Optional[bool] = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
    def backup_target_test_connection(
        self,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
