from urllib.parse import urljoin

import requests
from pkg_resources import parse_version
from requests.packages.urllib3.util.retry import Retry

from .managers import (
    CloudCredentialManager, ClusterRegistrationTokenManager, HarvesterConfigManager,
    KubeConfigManager, MgmtClusterManager, SecretManager, SettingManager,
    ClusterManager, NodeTemplateManager, NodePoolManager, UserManager,
    ChartManager, ClusterDeploymentManager, ClusterServiceManager, PVCManager
)


class RancherAPI:
    # not used
    first_login = "v1/management.cattle.io.setting/first-login"
    reset_password = "v3/users"

    @classmethod
    def login(cls, endpoint, user, passwd, session=None, ssl_verify=True):
        api = cls(endpoint, session=session)
        api.session.verify = ssl_verify
        api.authenticate(user, passwd)

        return api

    def __init__(self, endpoint, token=None, session=None):
        self.session = session or requests.Session()
        self.session.headers.update(Authorization=token or "")

        if session is None:
            self.set_retries()

        self._version = None
        self.raw_version = None

        self.endpoint = endpoint
        self.users = UserManager(self)
        self.cloud_credentials = CloudCredentialManager(self)
        self.cluster_registration_tokens = ClusterRegistrationTokenManager(self)
        self.harvester_configs = HarvesterConfigManager(self)
        self.kube_configs = KubeConfigManager(self)
        self.mgmt_clusters = MgmtClusterManager(self)
        self.secrets = SecretManager(self)
        self.settings = SettingManager(self)
        self.clusters = ClusterManager(self)
        self.node_templates = NodeTemplateManager(self)
        self.node_pools = NodePoolManager(self)
        self.charts = ChartManager(self)
        self.cluster_deployments = ClusterDeploymentManager(self)
        self.cluster_services = ClusterServiceManager(self)
        self.pvcs = PVCManager(self)

    @property
    def cluster_version(self):
        if not self._version:
            code, data = self.settings.get('server-version')
            ver = data['value']
            # XXX: fix master-xxx-head to 8.8.8, need the API fix the problem
            self._version = parse_version('8.8.8' if 'master' in ver else ver)

            self.raw_version = ver
        return self._version

    def __repr__(self):
        return f"RancherAPI({self.endpoint!r}, {self.session.headers['Authorization']!r})"

    def _get(self, path, **kwargs):
        url = urljoin(self.endpoint, path)
        resp = self.session.get(url, **kwargs)
        return resp

    def _post(self, path, **kwargs):
        url = urljoin(self.endpoint, path)
        resp = self.session.post(url, **kwargs)
        return resp

    def _put(self, path, **kwargs):
        url = urljoin(self.endpoint, path)
        resp = self.session.put(url, **kwargs)
        return resp

    def _delete(self, path, **kwargs):
        url = urljoin(self.endpoint, path)
        resp = self.session.delete(url, **kwargs)
        return resp

    def authenticate(self, user, passwd, **kwargs):
        path = "v3-public/localProviders/local?action=login"
        r = self._post(path, json=dict(username=user, password=passwd), **kwargs)
        try:
            assert r.status_code == 201, "Failed to authenticate"
        except AssertionError:
            pass  # TODO: Log authenticate error
        else:
            self.token = r.json()['token']
            self.session.headers.update(Authorization=f"Bearer {self.token}")
            self._version = None
        return r.json()

    def set_retries(self, times=5, status_forcelist=(500, 502, 504), **kwargs):
        kwargs.update(backoff_factor=kwargs.get('backoff_factor', 10.0),
                      total=kwargs.get('total', times),
                      status_forcelist=status_forcelist)
        retry_strategy = Retry(**kwargs)
        adapter = requests.adapters.HTTPAdapter(max_retries=retry_strategy)
        self.session.mount("https://", adapter)
        self.session.mount("http://", adapter)

    def generate_kubeconfig(self, harvester_id, harvester_name):
        # kubeconfig for provider
        # https://registry.terraform.io/providers/rancher/rancher2/3.1.1/docs/resources/cluster_v2
        url = f'{self.endpoint}/k8s/clusters/{harvester_id}/v1/harvester/kubeconfig'
        access_key, secret_key = self.token.split(":")
        auth = (access_key, secret_key)
        data = {
            "clusterRoleName": "harvesterhci.io:cloudprovider",
            "namespace": "default",
            "serviceAccountName": harvester_name
        }
        res = requests.post(url, auth=auth, json=data, verify=False)
        kubeconfig = res.text.replace("\\n", "\n").strip('"')
        return kubeconfig
