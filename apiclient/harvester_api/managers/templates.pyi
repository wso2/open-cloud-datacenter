from typing import ClassVar, Optional, TypeAlias

from requests.models import Response

from .base import BaseManager

United_S: TypeAlias = str


class TemplateManager(BaseManager):
    PATH_fmt: ClassVar[str]
    VER_PATH_fmt: ClassVar[str]

    def create_data(
        self,
        name: str,
        namespace: str,
        description: str
    ) -> dict:
        """
        """
    def create_version_data(
        self,
        name: str,
        namespace: str,
        cpu: int | float | United_S,
        mem: int | float | United_S,
        disk_name: str
    ) -> dict:
        """
        """
    def get(
        self,
        name: str = ...,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def get_version(
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
        namespace: str = ...,
        description: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def update(
        self,
        name: str,
        namespace: str = ...,
        *,
        raw: Optional[bool] = ...,
        **options
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
