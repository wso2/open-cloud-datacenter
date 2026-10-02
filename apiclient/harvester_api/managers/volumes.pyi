from typing import ClassVar, Optional, Type

from requests.models import Response

from harvester_api.models.volumes import VolumeSpec
from .base import BaseManager


class VolumeManager(BaseManager):
    PATH_fmt: ClassVar[str]
    Spec: Type[VolumeSpec]

    def get(
        self,
        name: str = ...,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def create(
        self,
        name: str,
        volume_spec: Type[VolumeSpec],
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def update(
        self,
        name: str,
        volume_spec: Type[VolumeSpec],
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        as_json: Optional[bool] = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
    def delete(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
