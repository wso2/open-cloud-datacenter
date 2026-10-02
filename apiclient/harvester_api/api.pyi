from typing import Type, TypeVar, TypeAlias, Optional, NoReturn, Iterable, ClassVar, Tuple

from packaging import version
from requests import Session

from . import managers
from .managers.base import DEFAULT_NAMESPACE


API_T = TypeVar('API_T', bound='HarvesterAPI')
Version: TypeAlias = version._BaseVersion
Url: TypeAlias = str


class HarvesterAPI:
    """
    """
    API_VERSION: ClassVar[str]

    session: Session
    endpoint: Url
    hosts: managers.HostManager
    keypairs: managers.KeypairManager
    images: managers.ImageManager
    networks: managers.NetworkManager
    volumes: managers.VolumeManager
    templates: managers.TemplateManager
    supportbundle: managers.SupportBundleManager
    settings: managers.SettingManager
    clusternetworks: managers.ClusterNetworkManager
    vms: managers.VirtualMachineManager
    backups: managers.BackupManager
    vm_snapshots: managers.VirtualMachineSnapshotManager
    scs: managers.StorageClassManager

    versions: managers.VersionManager
    upgrades: managers.UpgradeManager
    lhreplicas: managers.LonghornReplicaManager
    lhvolumes: managers.LonghornVolumeManager
    lhbackupvolumes: managers.LonghornBackupVolumeManager

    @classmethod
    def login(
        cls: Type[API_T],
        endpoint: Url,
        user: str,
        passwd: str,
        session: Optional[Session] = ...,
        ssl_verify: bool = ...
    ) -> API_T:
        """
        """
    def __init__(
        self,
        endpoint: Url,
        token: Optional[str] = ...,
        session: Optional[Session] = ...
    ) -> NoReturn:
        """
        """
    @property
    def cluster_version(self) -> Version:
        """
        """
    def load_managers(self, target_version: Version | str = "0.0.0") -> NoReturn:
        """
        """
    def get_url(self,
                path: str) -> Url:
        """
        """
    def authenticate(self, user: str, passwd: str, **kwargs) -> dict:
        """
        """
    def set_retries(
        self,
        times: int = 5,
        status_forcelist: Iterable[int] = ...,
        **kwargs
    ) -> NoReturn:
        """
        """
    def generate_kubeconfig(self) -> str:
        """
        """
    def get_pods(self, name: str = "", namespace: str = DEFAULT_NAMESPACE) -> Tuple[int, dict]:
        """
        """
    def get_apps_catalog(
        self, name: str = "", namespace: str = DEFAULT_NAMESPACE
    ) -> Tuple[int, dict]:
        """
        """
    def get_crds(self, name: str = "") -> Tuple[int, dict]:
        """
        """
