from __future__ import annotations
from typing import ClassVar, Optional, List, NoReturn, TypeAlias, Type

United_S: TypeAlias = str


class VMSpec:
    """
    """

    cpu_sockets: ClassVar[int]
    cpu_threads: ClassVar[int]
    run_strategy: ClassVar[str]
    eviction_strategy: ClassVar[str]
    hostname: ClassVar[str]
    machine_type: ClassVar[str]
    usbtablet: ClassVar[bool]

    cpu_cores: int
    cpu_model: Optional[str]
    memory: int | United_S
    description: str
    reserved_mem: int | float | United_S
    os_type: Optional[str]
    networks: List[dict]

    def __init__(
        self,
        cpu_cores: int,
        memory: int | United_S,
        description: str = ...,
        reserved_mem: int | float | United_S = ...,
        os_type: str = ...,
        mgmt_network: Optional[bool] = ...,
        guest_agent: Optional[bool] = ...
    ) -> NoReturn:
        """
        """
    @property
    def mgmt_network(self) -> bool:
        """
        """
    @mgmt_network.setter
    def mgmt_network(self, enable: bool) -> NoReturn:
        """
        """
    @property
    def acpi(self) -> bool | None:
        """
        """
    @acpi.setter
    def acpi(self, enable: bool) -> NoReturn:
        """
        """
    @property
    def efi_boot(self) -> bool:
        """
        """
    @efi_boot.setter
    def efi_boot(self, enable: bool) -> NoReturn:
        """
        """
    @property
    def secure_boot(self) -> bool:
        """
        """
    @secure_boot.setter
    def secure_boot(self, enable: bool) -> NoReturn:
        """
        """
    @property
    def guest_agent(self) -> bool:
        """
        """
    @guest_agent.setter
    def guest_agent(self, enable: bool) -> NoReturn:
        """
        """
    @property
    def user_data(self) -> str:
        """
        """
    @user_data.setter
    def user_data(self, val: str) -> NoReturn:
        """
        """
    @property
    def network_data(self) -> str:
        """
        """
    @network_data.setter
    def network_data(self, val: str) -> NoReturn:
        """
        """
    def add_cd_rom(
        self,
        name: str,
        image_id: str,
        size: int | float | United_S = ...,
        bus: str = ...,
        image_uid: Optional[str] = ...
    ) -> dict:
        """
        """
    def add_image(
        self,
        name: str,
        image_id: str,
        size: int | float | United_S = ...,
        bus: str = ...,
        type: str = ...,
        image_uid: Optional[str] = ...
    ) -> dict:
        """
        """
    def add_container(
        self,
        name: str,
        docker_image: str,
        bus: str = ...,
        type: str = ...
    ) -> dict:
        """
        """
    def add_volume(
        self,
        name: str,
        size: int | float | United_S,
        type: str = ...,
        bus: str = ...,
        storage_cls: str = ...
    ) -> dict:
        """
        """
    def add_existing_volume(
        self,
        name: str,
        volume_name: str,
        type: str = ...
    ) -> dict:
        """
        """
    def add_network(
        self,
        name: str,
        net_uid: str,
        model: str = ...,
        mac_addr: Optional[str] = ...
    ) -> dict:
        """
        """
    def to_dict(
        self,
        name: str,
        namespace: str,
        hostname: str = ...
    ) -> dict:
        """
        """
    @classmethod
    def from_dict(cls, data: dict) -> Type[VMSpec]:
        """
        """
