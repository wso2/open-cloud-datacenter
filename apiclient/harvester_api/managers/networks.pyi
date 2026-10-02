from typing import ClassVar, Optional, NoReturn

from requests.models import Response

from .base import BaseManager


class NetworkManager(BaseManager):
    PATH_fmt: ClassVar[str]
    API_VERSION: ClassVar[str]

    def create_data(
        self,
        name: str,
        namespace: str,
        vlan_id: int,
        bridge_name: str
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
        vlan_id: int,
        namespace: str = ...,
        *,
        cluster_network: Optional[str] = ...,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def update(self, *args, **kwargs) -> NoReturn:
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
