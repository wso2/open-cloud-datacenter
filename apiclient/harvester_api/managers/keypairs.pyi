from typing import ClassVar, Optional, NoReturn

from requests.models import Response

from .base import BaseManager


class KeypairManager(BaseManager):
    PATH_fmt: ClassVar[str]

    def create_data(
        self,
        name: str,
        namespace: str,
        public_key: str
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
        public_key: str,
        namespace: str = ...,
        *,
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
