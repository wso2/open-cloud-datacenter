from __future__ import annotations
from typing import Final, NoReturn, ClassVar, Type, TypeAlias

from packaging import version

from .api import HarvesterAPI

Version: TypeAlias = version._BaseVersion

DEFAULT_NAMESPACE: Final[str]


def merge_dict(src: dict, dest: dict) -> dict:
    """
    """


class BaseManager:
    _sub_classes: ClassVar[dict[Type[BaseManager], list[Type[BaseManager]]]]
    support_to: ClassVar[str]

    @classmethod
    def is_support(cls, target_version: Version | str) -> bool:
        """
        """
    @classmethod
    def for_version(cls, version: str) -> Type[BaseManager]:
        """
        """
    def __init__(self, api: HarvesterAPI) -> NoReturn:
        """
        """
    @property
    def api(self) -> HarvesterAPI:
        """
        """
