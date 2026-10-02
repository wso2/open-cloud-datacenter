from typing import ClassVar, Optional, NoReturn
from collections.abc import Mapping

from requests.models import Response

from .base import BaseManager


class HostManager(BaseManager):
    PATH_fmt: ClassVar[str]
    METRIC_fmt: ClassVar[str]

    def get(
        self,
        name: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def create(self, *args, **kwargs) -> NoReturn:
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
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def get_metrics(
        self,
        name: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def maintenance_mode(
        self,
        name: str,
        enable: Optional[bool] = ...
    ) -> dict:
        """
        """
