from typing import ClassVar, Optional, NoReturn, Tuple

from requests.models import Response

from .base import BaseManager


class SupportBundleManager(BaseManager):
    PATH_fmt: ClassVar[str]
    DL_fmt: ClassVar[str]

    def create_data(
        self,
        name: str,
        description: str,
        issue_url: str
    ) -> dict:
        """
        """
    def get(
        self,
        uid: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def create(
        self,
        name: str,
        description: str = ...,
        issue_url: str = ...,
        *,
        raw: Optional[bool] = ...
    ) -> dict | Response:
        """
        """
    def download(self, uid: str) -> Tuple[int, str]:
        """
        """
    def update(self, *args, **kwargs) -> NoReturn:
        """
        """
    def delete(self, uid: str, *, raw: Optional[bool] = ...) -> dict | Response:
        """
        """
