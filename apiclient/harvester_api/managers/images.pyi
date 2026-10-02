from typing import ClassVar, Optional
from pathlib import Path
from collections.abc import Mapping

from requests.models import Response

from .base import BaseManager


class ImageManager(BaseManager):
    PATH_fmt: ClassVar[str]
    UPLOAD_fmt: ClassVar[str]

    def create_data(
        self,
        name: str,
        url: str,
        desc: str,
        stype: str,
        namespace: str,
        imageChecksum=str,
        display_name: str = ...
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
    def create(
        self,
        name: str,
        namespace: str = ...,
        **kwargs
    ) -> dict | Response:
        """
        """
    def create_by_url(
        self,
        name: str,
        url: str,
        imageChecksum: str,
        namespace: str = ...,
        description: str = ...,
        display_name: str = ...
    ) -> dict | Response:
        """
        """
    def create_by_file(
        self,
        name: str,
        filepath: str | Path,
        namespace: str = ...,
        description: str = ...,
        display_name: str = ...
    ) -> dict | Response:
        """
        """
    def update(
        self,
        name: str,
        data: Mapping,
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
