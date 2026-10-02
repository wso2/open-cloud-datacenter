from __future__ import annotations
from typing import ClassVar, Optional, NoReturn, Type, TypeAlias
from collections.abc import Iterable

United_S: TypeAlias = str


class VolumeSpec:
    """
    """

    volume_mode: ClassVar[str]

    access_modes: Iterable[str]
    size: int | float | United_S
    storage_cls: str
    description: str
    annotations: dict

    def __init__(
        self,
        size: int | float | United_S,
        storage_cls: Optional[str] = ...,
        description: Optional[str] = ...,
        annotations: Optional[dict] = ...
    ) -> NoReturn:
        """
        """
    def to_dict(
        self,
        name: str,
        namespace: str,
        image_id: Optional[str] = ...
    ) -> dict:
        """
        """
    @classmethod
    def from_dict(cls, data: dict) -> Type[VolumeSpec]:
        """
        """
