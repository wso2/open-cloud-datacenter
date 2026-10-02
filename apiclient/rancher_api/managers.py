import base64
import json
import yaml
from weakref import ref
from collections.abc import Mapping
from urllib.parse import urlencode

from .models import UserSpec, ChartSpec, LBServiceSpec


DEFAULT_NAMESPACE = "default"
FLEET_DEFAULT_NAMESPACE = "fleet-default"


def merge_dict(src, dest):
    for k, v in src.items():
        if isinstance(dest.get(k), dict) and isinstance(v, dict):
            merge_dict(v, dest[k])
        else:
            dest[k] = v
    return dest


class BaseManager:
    def __init__(self, api):
        self._api = ref(api)

    @property
    def api(self):
        if self._api() is None:
            raise ReferenceError("API object no longer exists")
        return self._api()

    def _delegate(self, meth, path, *, raw=False, **kwargs):
        func = getattr(self.api, meth)
        resp = func(path, **kwargs)

        if raw:
            return resp
        try:
            if "json" in resp.headers.get('Content-Type', ""):
                rval = resp.json()
            else:
                rval = resp.text
            return resp.status_code, rval
        except json.decoder.JSONDecodeError as e:
            return resp.status_code, dict(error=e, response=resp)

    def _get(self, path, *, raw=False, **kwargs):
        return self._delegate("_get", path, raw=raw, **kwargs)

    def _create(self, path, *, raw=False, **kwargs):
        return self._delegate("_post", path, raw=raw, **kwargs)

    def _update(self, path, data, *, raw=False, as_json=True, **kwargs):
        if as_json:
            kwargs.update(json=data)
        else:
            kwargs.update(data=data)

        return self._delegate("_put", path, raw=raw, **kwargs)

    def _delete(self, path, *, raw=False, **kwargs):
        return self._delegate("_delete", path, raw=raw, **kwargs)


class UserManager(BaseManager):
    PATH_fmt = "v3/users/{uid}"
    ROLE_fmt = "v3/globalrolebindings/{uid}"

    Spec = UserSpec

    def me(self):
        return self.get("", params=dict(me='true'))[1]['data'][0]

    def get(self, uid="", *, raw=False, **kwargs):
        path = self.PATH_fmt.format(uid=uid)
        return self._get(path, raw=raw, **kwargs)

    def get_by_name(self, name, *, raw=False):
        resp = self.get(raw=raw, params=dict(username=name))
        if raw:
            return resp
        try:
            code, data = resp
            return code, data['data'][0]
        except IndexError:
            return 404, dict(type="error", status=404, code="NotFound",
                             message=f"username {name!r} not found")

    def create(self, username, spec, *, raw=False):
        if isinstance(spec, self.Spec):
            spec = spec.to_dict(username)
        path = self.PATH_fmt.format(uid="")
        return self._create(path, json=spec, raw=raw)

    def update(self, uid, spec, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(uid=uid)
        _, user = self.get(uid)
        if isinstance(spec, self.Spec):
            spec = spec.to_dict(user['username'])
        if isinstance(spec, Mapping) and as_json:
            spec = merge_dict(spec, user)
        return self._update(path, spec, raw=raw, as_json=as_json, **kwargs)

    def update_password(self, uid, passwd, *, raw=False):
        path = self.PATH_fmt.format(uid=uid)
        data = dict(newPassword=passwd)
        return self._create(path, raw=raw, json=data, params=dict(action="setpassword"))

    def delete(self, uid, *, raw=False):
        path = self.PATH_fmt.format(uid=uid)
        return self._delete(path, raw=raw)

    def get_roles(self, uid, *, raw=False, **kwargs):
        path = self.ROLE_fmt.format(uid="")
        params = merge_dict(dict(userId=uid), kwargs.pop('params', {}))
        return self._get(path, params=params, raw=raw, **kwargs)

    def add_role(self, uid, role_id, *, raw=False):
        path = self.ROLE_fmt.format(uid="")
        data = dict(type="globalRoleBinding", userId=uid, globalRoleId=role_id)
        return self._create(path, json=data, raw=raw)

    def delete_role(self, uid, role_id, *, raw=False):
        try:
            code, data = self.get_roles(uid, params=dict(globalRoleId=role_id))
            ruid = data['data'][0]['id']
            return self._delete(self.ROLE_fmt.format(uid=ruid), raw=raw)
        except IndexError:
            return 404, dict(type='error', status=404, code='NotFound',
                             message=f"User {uid!r} haven't Role {role_id!r}")
        except KeyError:
            return code, data


class SettingManager(BaseManager):
    # server-version
    PATH_fmt = "apis/management.cattle.io/v3/settings/{name}"
    # "v1/harvesterhci.io.settings/{name}"

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(name=name))


class MgmtClusterManager(BaseManager):
    PATH_fmt = "v1/provisioning.cattle.io.clusters{ns}{uid}"

    def create_data(self, cluster_name, cloud_provider_config_id, hostname_prefix,
                    harvester_config_name, k8s_version, cloud_credential_id, quantity):

        return {
            "type": "provisioning.cattle.io.cluster",
            "metadata": {
                "namespace": FLEET_DEFAULT_NAMESPACE,
                "name": cluster_name
            },
            "spec": {
                "rkeConfig": {
                    "chartValues": {
                        "rke2-calico": {},
                        "harvester-cloud-provider": {
                            "clusterName": cluster_name,
                            "cloudConfigPath": "/var/lib/rancher/rke2/etc/config-files/cloud-provider-config"  # noqa
                        }
                    },
                    "upgradeStrategy": {
                        "controlPlaneConcurrency": "1",
                        "controlPlaneDrainOptions": {
                            "deleteEmptyDirData": True,
                            "disableEviction": False,
                            "enabled": False,
                            "force": False,
                            "gracePeriod": -1,
                            "ignoreDaemonSets": True,
                            "ignoreErrors": False,
                            "skipWaitForDeleteTimeoutSeconds": 0,
                            "timeout": 120
                        },
                        "workerConcurrency": "1",
                        "workerDrainOptions": {
                            "deleteEmptyDirData": True,
                            "disableEviction": False,
                            "enabled": False,
                            "force": False,
                            "gracePeriod": -1,
                            "ignoreDaemonSets": True,
                            "ignoreErrors": False,
                            "skipWaitForDeleteTimeoutSeconds": 0,
                            "timeout": 120
                        }
                    },
                    "machineGlobalConfig": {
                        "cni": "calico",
                        "disable-kube-proxy": False,
                        "etcd-expose-metrics": False,
                        "profile": None
                    },
                    "machineSelectorConfig": [
                        {
                            "config": {
                                "cloud-provider-config": f"secret://{cloud_provider_config_id}",
                                "cloud-provider-name": "harvester",
                                "protect-kernel-defaults": False
                            }
                        }
                    ],
                    "etcd": {
                        "disableSnapshots": False,
                        "s3": None,
                        "snapshotRetention": 5,
                        "snapshotScheduleCron": "0 */5 * * *"
                    },
                    "registries": {
                        "configs": {},
                        "mirrors": {}
                    },
                    "machinePools": [
                        {
                            "name": "pool1",
                            "etcdRole": True,
                            "controlPlaneRole": True,
                            "workerRole": True,
                            "hostnamePrefix": hostname_prefix,
                            "labels": {},
                            "quantity": quantity,
                            "unhealthyNodeTimeout": "0m",
                            "machineConfigRef": {
                                "kind": "HarvesterConfig",
                                "name": harvester_config_name
                            }
                        }
                    ]
                },
                "machineSelectorConfig": [
                    {
                        "config": {}
                    }
                ],
                "kubernetesVersion": k8s_version,
                "defaultPodSecurityPolicyTemplateName": "",
                "cloudCredentialSecretName": cloud_credential_id,
                "localClusterAuthEndpoint": {
                    "enabled": False,
                    "caCerts": "",
                    "fqdn": ""
                }
            }
        }

    def get(self, name="", *, raw=False):
        if name == "":
            return self._get(self.PATH_fmt.format(uid="", ns=""), raw=raw)
        return self._get(
            self.PATH_fmt.format(uid=f"/{name}", ns=f"/{FLEET_DEFAULT_NAMESPACE}"),
            raw=raw
        )

    def create(self, name, cloud_provider_config_id, hostname_prefix,
               harvester_config_name, k8s_version, cloud_credential_id,
               quantity=1, *, raw=False):
        data = self.create_data(name, cloud_provider_config_id, hostname_prefix,
                                harvester_config_name, k8s_version, cloud_credential_id, quantity)
        return self._create(self.PATH_fmt.format(uid="", ns=""), json=data, raw=raw)

    def create_harvester(self, name, *, raw=False):
        return self._create(
            self.PATH_fmt.format(uid="", ns=""),
            json={
                "type": "provisioning.cattle.io.cluster",
                "metadata": {
                    "namespace": FLEET_DEFAULT_NAMESPACE,
                    "labels": {
                        "provider.cattle.io": "harvester"
                    },
                    "name": name,
                },
                "spec": {}
            },
            raw=raw
        )

    def update(self, name, data, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(uid=f"/{name}", ns=f"/{FLEET_DEFAULT_NAMESPACE}")
        return self._update(path, data, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, namespace=FLEET_DEFAULT_NAMESPACE, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=f"/{name}", ns=f"/{namespace}"))


class ClusterRegistrationTokenManager(BaseManager):
    PATH_fmt = "v3/clusterRegistrationTokens/{uid}:default-token"

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name), raw=raw)


class CloudCredentialManager(BaseManager):
    PATH_fmt = "v3/cloudcredentials{uid}"

    def create_data(self, name, kubeconfig, cluster_id=""):
        if cluster_id == "":
            harvester_credential_config = {
                "clusterType": "external",
                "kubeconfigContent": kubeconfig
            }
        else:
            harvester_credential_config = {
                "clusterType": "imported",
                "clusterId": cluster_id,
                "kubeconfigContent": kubeconfig
            }

        return {
            "type": "provisioning.cattle.io/cloud-credential",
            "metadata": {
                "generateName": "cc-",
                "namespace": FLEET_DEFAULT_NAMESPACE
            },
            "_name": name,
            "annotations": {
                "provisioning.cattle.io/driver": "harvester"
            },
            "harvestercredentialConfig": harvester_credential_config,
            "_type": "provisioning.cattle.io/cloud-credential",
            "name": name
        }

    def create(self, name, kubeconfig, cluster_id="", *, raw=False):
        data = self.create_data(name, kubeconfig, cluster_id)
        return self._create(self.PATH_fmt.format(uid="", ns=""), json=data, raw=raw)

    def get(self, name="", *, raw=False, **kwargs):
        uid = f"/{name}" if name else ""

        if not kwargs:
            return self._get(self.PATH_fmt.format(uid=uid), raw=raw)

        return self._get(self.PATH_fmt.format(uid=f"{uid}?{urlencode(kwargs)}"), raw=raw)

    def delete(self, name, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=f"/{name}"), raw=raw)


class KubeConfigManager(BaseManager):
    PATH_fmt = "k8s/clusters/{cluster_id}/v1/harvester/kubeconfig"

    def create_data(self, name):
        return {
            "clusterRoleName": "harvesterhci.io:cloudprovider",
            "namespace": DEFAULT_NAMESPACE,
            "serviceAccountName": name
        }

    def create(self, name, cluster_id, *, raw=False):
        data = self.create_data(name)
        return self._create(self.PATH_fmt.format(cluster_id=cluster_id), json=data, raw=raw)


class ChartManager(BaseManager):
    PATH_fmt = "k8s/clusters/{cluster_id}/v1/catalog.cattle.io.apps"
    CREATE_fmt = "k8s/clusters/{cluster_id}/v1/catalog.cattle.io.clusterrepos/rancher-charts"

    def get(self, cluster_id, namespace, name, raw=False):
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        if namespace:
            url = f"{url}/{namespace}"
            if name:
                url = f"{url}/{name}"
        return self._get(url, raw=raw)

    def create(self, cluster_id, namespace, name, raw=False):
        url = self.CREATE_fmt.format(cluster_id=cluster_id) + "?action=install"
        data = ChartSpec(cluster_id, namespace, name).to_dict()
        return self._create(url, json=data, raw=raw)


class ClusterDeploymentManager(BaseManager):
    PATH_fmt = "k8s/clusters/{cluster_id}/v1/apps.deployments"

    def get(self, cluster_id, namespace=DEFAULT_NAMESPACE, name="", raw=False):
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        if namespace:
            url = f"{url}/{namespace}"
            if name:
                url = f"{url}/{name}"
        return self._get(url, raw=raw)

    def _create_data(self, namespace, name, image, pvc):
        workloadselector = f"apps.deployment-{namespace}-{name}"
        volumes, volume_mounts = [], []
        if pvc:
            volumes = [{
                "name": f"vol-{pvc}",
                "persistentVolumeClaim": {
                    "claimName": pvc
                }
            }]
            volume_mounts = [{
                "mountPath": "/data",
                "name": f"vol-{pvc}"
            }]

        return {
            "type": "apps.deployment",
            "metadata": {
                "namespace": namespace,
                "labels": {
                    "workload.user.cattle.io/workloadselector": workloadselector
                },
                "name": name
            },
            "spec": {
                "replicas": 1,
                "template": {
                    "spec": {
                        "restartPolicy": "Always",
                        "containers": [
                            {
                                "imagePullPolicy": "Always",
                                "name": "container-0",
                                "securityContext": {
                                    "runAsNonRoot": False,
                                    "readOnlyRootFilesystem": False,
                                    "privileged": False,
                                    "allowPrivilegeEscalation": False
                                },
                                "_init": False,
                                "volumeMounts": volume_mounts,
                                "__active": True,
                                "image": image
                            }
                        ],
                        "initContainers": [],
                        "imagePullSecrets": [],
                        "volumes": volumes,
                        "affinity": {}
                    },
                    "metadata": {
                        "labels": {
                            "name": name,
                            "workload.user.cattle.io/workloadselector": workloadselector
                        },
                        "namespace": namespace
                    }
                },
                "selector": {
                    "matchLabels": {
                        "workload.user.cattle.io/workloadselector": workloadselector
                    }
                }
            }
        }

    def create(self, cluster_id, namespace, name, image, pvc="", raw=False):
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        data = self._create_data(namespace, name, image, pvc)
        return self._create(url, json=data, raw=raw)

    def delete(self, cluster_id, namespace, name, raw=False):
        url = f"{self.PATH_fmt.format(cluster_id=cluster_id)}/{namespace}/{name}"
        return self._delete(url, raw=raw)


class ClusterServiceManager(BaseManager):
    PATH_fmt = "k8s/clusters/{cluster_id}/v1/services"

    def get(self, cluster_id, name="", namespace=DEFAULT_NAMESPACE, raw=False):
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        if namespace:
            url = f"{url}/{namespace}"
            if name:
                url = f"{url}/{name}"
        return self._get(url, raw=raw)

    def create(
        self, cluster_id, name, ipam, selector=None, namespace=DEFAULT_NAMESPACE, raw=False
    ):
        if selector is None:
            selector = {}
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        data = LBServiceSpec(namespace, name, ipam, selector).to_dict()
        return self._create(url, json=data, raw=raw)

    def delete(self, cluster_id, name, namespace=DEFAULT_NAMESPACE, raw=False):
        url = f"{self.PATH_fmt.format(cluster_id=cluster_id)}/{namespace}/{name}"
        return self._delete(url, raw=raw)


class PVCManager(BaseManager):
    PATH_fmt = "k8s/clusters/{cluster_id}/v1/persistentvolumeclaims"

    def get(self, cluster_id, name="", namespace=DEFAULT_NAMESPACE, raw=False):
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        if namespace:
            url = f"{url}/{namespace}"
            if name:
                url = f"{url}/{name}"
        return self._get(url, raw=raw)

    def create(self, cluster_id, name, namespace=DEFAULT_NAMESPACE, raw=False):
        url = self.PATH_fmt.format(cluster_id=cluster_id)
        data = {
            "type": "persistentvolumeclaim",
            "metadata": {
                "namespace": namespace,
                "name": name
            },
            "spec": {
                "accessModes": [
                    "ReadWriteOnce"
                ],
                "volumeName": "",
                "resources": {
                    "requests": {
                        "storage": "1Gi"
                    }
                }
            }
        }
        return self._create(url, json=data, raw=raw)

    def delete(self, cluster_id, name, namespace=DEFAULT_NAMESPACE, raw=False):
        url = f"{self.PATH_fmt.format(cluster_id=cluster_id)}/{namespace}/{name}"
        return self._delete(url, raw=raw)


class SecretManager(BaseManager):
    PATH_fmt = "v1/secrets"

    def create_data(self, name, namespace, data, annotations=None):
        annotations = annotations or {}

        for key, value in data.items():
            data[key] = base64.b64encode(value.encode('UTF-8')).decode('UTF-8')

        return {
            "type": "secret",
            "metadata": {
                "namespace": namespace,
                "name": name,
                "annotations": annotations
            },
            "data": data
        }

    def create(self, name, data, namespace=FLEET_DEFAULT_NAMESPACE,
               annotations=None, *, raw=False):
        data = self.create_data(name, namespace, data, annotations=annotations)
        return self._create(self.PATH_fmt, json=data, raw=raw)


class HarvesterConfigManager(BaseManager):
    PATH_fmt = "v1/rke-machine-config.cattle.io.harvesterconfigs/fleet-default"

    def _inject_guest_agent(self, user_data):
        cmd = 'systemctl enable --now qemu-guest-agent.service'
        userdata = yaml.safe_load(user_data) or dict()
        pkgs = userdata.get('packages', [])
        runcmds = [' '.join(c) for c in userdata.get('runcmd', [])]
        if 'qemu-guest-agent' not in pkgs:
            userdata.setdefault('packages', []).append('qemu-guest-agent')

        if cmd not in runcmds:
            userdata.setdefault('runcmd', []).append(cmd.split())
        return f"#cloud-config\n{yaml.dump(userdata)}"

    def create_data(self, name, cpus, mems, disks, image_id, network_id,
                    ssh_user, user_data, network_data, vm_namespace=DEFAULT_NAMESPACE):
        user_data = self._inject_guest_agent(user_data)

        return {
            "cpuCount": cpus,
            "diskSize": disks,
            "imageName": image_id,
            "memorySize": mems,
            "metadata": {
                "name": name,
                "namespace": FLEET_DEFAULT_NAMESPACE,
            },
            "networkName": network_id,
            "sshUser": ssh_user,
            "userData": base64.b64encode(user_data.encode('UTF-8')).decode('UTF-8'),
            "networkData": base64.b64encode(network_data.encode('UTF-8')).decode('UTF-8'),
            "vmNamespace": vm_namespace,
            "type": "rke-machine-config.cattle.io.harvesterconfig"
        }

    def create(self, name, cpus, mems, disks, image_id, network_id,
               ssh_user, vm_namespace=DEFAULT_NAMESPACE, user_data="",
               network_data="", *, raw=False):
        data = self.create_data(
            name=name,
            cpus=cpus,
            mems=mems,
            disks=disks,
            image_id=image_id,
            network_id=network_id,
            ssh_user=ssh_user,
            vm_namespace=vm_namespace,
            user_data=user_data,
            network_data=network_data
        )
        return self._create(self.PATH_fmt, json=data, raw=raw)


class NodeTemplateManager(BaseManager):
    PATH_fmt = "v3/nodeTemplates/{uid}"

    def _inject_guest_agent(self, user_data):
        cmd = 'systemctl enable --now qemu-guest-agent.service'
        userdata = yaml.safe_load(user_data) or dict()
        pkgs = userdata.get('packages', [])
        runcmds = [' '.join(c) for c in userdata.get('runcmd', [])]
        if 'qemu-guest-agent' not in pkgs:
            userdata.setdefault('packages', []).append('qemu-guest-agent')

        if cmd not in runcmds:
            userdata.setdefault('runcmd', []).append(cmd.split())
        return f"#cloud-config\n{yaml.dump(userdata)}"

    def create_data(self, name, cpus, mems, disks, image_id, network_id,
                    ssh_user, cloud_credential_id, user_data, network_data,
                    engine_url, vm_namespace=DEFAULT_NAMESPACE):
        user_data = self._inject_guest_agent(user_data)

        return {
            "useInternalIpAddress": True,
            "type": "nodeTemplate",
            "engineInstallURL": engine_url,
            "engineRegistryMirror": [],
            "harvesterConfig": {
                "cloudConfig": "",
                "clusterId": "",
                "clusterType": "",
                "cpuCount": cpus,
                "diskBus": "virtio",
                "diskSize": disks,
                "imageName": image_id,
                "keyPairName": "",
                "kubeconfigContent": "",
                "memorySize": mems,
                "networkData": network_data,
                "networkModel": "virtio",
                "networkName": network_id,
                "networkType": "dhcp",
                "sshPassword": "",
                "sshPort": "22",
                "sshPrivateKeyPath": "",
                "sshUser": ssh_user,
                "userData": user_data,
                "vmAffinity": "",
                "vmNamespace": vm_namespace,
                "type": "harvesterConfig"
            },
            "namespaceId": "fixme",  # fixme is a real parameter
            "cloudCredentialId": cloud_credential_id,
            "labels": {},
            "name": name
        }

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name), raw=raw)

    def create(self, name, cpus, mems, disks, image_id, network_id,
               ssh_user, cloud_credential_id, vm_namespace=DEFAULT_NAMESPACE,
               user_data="", network_data="", *, engine_url=None, raw=False):
        # TODO: need to align recommended in settings/engine-install-url
        engine_url = engine_url or 'https://get.docker.com'  # latest

        data = self.create_data(
            name=name,
            cpus=cpus,
            mems=mems,
            disks=disks,
            image_id=image_id,
            network_id=network_id,
            ssh_user=ssh_user,
            cloud_credential_id=cloud_credential_id,
            vm_namespace=vm_namespace,
            user_data=user_data,
            network_data=network_data,
            engine_url=engine_url
        )
        return self._create(self.PATH_fmt.format(uid="", ns=""), json=data, raw=raw)

    def delete(self, name, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name), raw=raw)


class ClusterManager(BaseManager):
    PATH_fmt = "v3/cluster/{uid}"
    PATH1_fmt = "v3/clusters/{uid}"

    def create_data(self, name, k8s_version, kubeconfig):

        return {
            "dockerRootDir": "/var/lib/docker",
            "enableClusterAlerting": False,
            "enableClusterMonitoring": False,
            "enableNetworkPolicy": False,
            "windowsPreferedCluster": False,
            "type": "cluster",
            "name": name,
            "rancherKubernetesEngineConfig": {
                "addonJobTimeout": 45,
                "enableCriDockerd": False,
                "ignoreDockerVersion": True,
                "rotateEncryptionKey": False,
                "sshAgentAuth": False,
                "type": "rancherKubernetesEngineConfig",
                "kubernetesVersion": k8s_version,
                "authentication": {
                    "strategy": "x509",
                    "type": "authnConfig"
                },
                "dns": {
                    "type": "dnsConfig",
                    "nodelocal": {
                            "type": "nodelocal",
                            "ip_address": "",
                            "node_selector": None,
                            "update_strategy": {}
                    }
                },
                "network": {
                    "mtu": 0,
                    "plugin": "canal",
                    "type": "networkConfig",
                    "options": {
                        "flannel_backend_type": "vxlan"
                    }
                },
                "ingress": {
                    "defaultBackend": False,
                    "defaultIngressClass": True,
                    "httpPort": 0,
                    "httpsPort": 0,
                    "provider": "nginx",
                    "type": "ingressConfig"
                },
                "monitoring": {
                    "provider": "metrics-server",
                    "replicas": 1,
                    "type": "monitoringConfig"
                },
                "services": {
                    "type": "rkeConfigServices",
                    "kubeApi": {
                        "alwaysPullImages": False,
                        "podSecurityPolicy": False,
                        "serviceNodePortRange": "30000-32767",
                        "type": "kubeAPIService",
                        "secretsEncryptionConfig": {
                            "enabled": False,
                            "type": "secretsEncryptionConfig"
                        }
                    },
                    "etcd": {
                        "creation": "12h",
                        "extraArgs": {
                            "heartbeat-interval": 500,
                            "election-timeout": 5000
                        },
                        "gid": 0,
                        "retention": "72h",
                        "snapshot": False,
                        "uid": 0,
                        "type": "etcdService",
                        "backupConfig": {
                            "enabled": True,
                            "intervalHours": 12,
                            "retention": 6,
                            "safeTimestamp": False,
                            "timeout": 300,
                            "type": "backupConfig"
                        }
                    }
                },
                "upgradeStrategy": {
                    "maxUnavailableControlplane": "1",
                    "maxUnavailableWorker": "10%",
                    "drain": "false",
                    "nodeDrainInput": {
                        "deleteLocalData": False,
                        "force": False,
                        "gracePeriod": -1,
                        "ignoreDaemonSets": True,
                        "timeout": 120,
                        "type": "nodeDrainInput"
                    },
                    "maxUnavailableUnit": "percentage"
                },
                "cloudProvider": {
                    "type": "cloudProvider",
                    "name": "harvester",
                    "harvesterCloudProvider": {
                        "cloudConfig": kubeconfig
                    }
                }
            },
            "localClusterAuthEndpoint": {
                "enabled": True,
                "type": "localClusterAuthEndpoint"
            },
            "labels": {},
            "scheduledClusterScan": {
                "enabled": False,
                "scheduleConfig": None,
                "scanConfig": None
            }
        }

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name), raw=raw)

    def create(self, name, k8s_version, kubeconfig, *, raw=False):
        data = self.create_data(name, k8s_version, kubeconfig)
        return self._create(self.PATH_fmt.format(uid=""), json=data, raw=raw)

    def delete(self, name, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name), raw=raw)

    def generate_kubeconfig(self, name, *, raw=False):
        return self._create(
            self.PATH1_fmt.format(uid=name),
            raw=raw,
            params={'action': 'generateKubeconfig'}
        )

    def explore(self, name):
        from .cluster_api import ClusterExploreAPI  # circular dependency
        return ClusterExploreAPI(self.api.endpoint, self.api.session, name)


class NodePoolManager(BaseManager):
    PATH_fmt = "v3/nodepool/{ns}{uid}"

    def create_data(self, cluster_id, node_template_id, hostname_prefix, quantity):

        return {
            "controlPlane": True,
            "deleteNotReadyAfterSecs": 0,
            "drainBeforeDelete": False,
            "etcd": True,
            "quantity": quantity,
            "worker": True,
            "type": "nodePool",
            "clusterId": cluster_id,
            "nodeTemplateId": node_template_id,
            "hostnamePrefix": hostname_prefix
        }

    def get(self, name="", ns="", *, raw=False):
        if name == "":
            return self._get(self.PATH_fmt.format(uid="", ns=""), raw=raw)
        return self._get(self.PATH_fmt.format(uid=f":{name}", ns=ns), raw=raw)

    def create(self, cluster_id, node_template_id, hostname_prefix, quantity=1, *, raw=False):
        data = self.create_data(cluster_id, node_template_id, hostname_prefix, quantity)
        return self._create(self.PATH_fmt.format(uid="", ns=""), json=data, raw=raw)

    def delete(self, name, ns, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=f":{name}", ns=ns), raw=raw)
