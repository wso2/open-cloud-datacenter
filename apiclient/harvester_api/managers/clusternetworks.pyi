from typing import ClassVar, Optional
from collections.abc import Mapping

from requests.models import Response

from .base import BaseManager


class ClusterNetworkManager(BaseManager):
    PATH_fmt: ClassVar[str]
    CONFIG_fmt: ClassVar[str]

    def create_data(
        self,
        name: str,
        description: str = ...,
        labels: Optional[dict] = ...,
        annotations: Optional[dict] = ...
    ) -> dict:
        """
        """
    def create_config_data(
        self,
        name: str,
        clusternetwork: str,
        *nics: str,
        bond_mode: Optional[str] = ...,
        hostname: Optional[str] = ...,
        miimon: Optional[int] = ...,
        mtu: Optional[int] = ...
    ) -> dict:
        """
        """
    def get(
        self,
        name: str = ...,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def create(
        self,
        name: str,
        description: str = ...,
        labels: Optional[dict] = ...,
        annotations: Optional[dict] = ...,
        *,
        raw: Optional[bool] = ...
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
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def get_config(
        self,
        name: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def create_config(
        self,
        name: str,
        clusternetwork: str,
        *nics: str,
        bond_mode: Optional[str] = ...,
        hostname: Optional[str] = ...,
        miimon: Optional[int] = ...,
        mtu: Optional[int] = ...,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def update_config(
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
    def delete_config(
        self,
        name: str,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
