import json

from pkg_resources import parse_version

from .base import DEFAULT_NAMESPACE, BaseManager


class NetworkManager(BaseManager):
    # get, create, update, delete
    PATH_fmt = "apis/{NETWORK_API}/namespaces/{ns}/network-attachment-definitions/{uid}"
    API_VERSION = "k8s.cni.cncf.io/v1"
    _KIND = "NetworkAttachmentDefinition"

    def _bridge_name(self, cluster_network=None):
        if cluster_network is not None:
            return f"{cluster_network}-br"
        if self.api.cluster_version >= parse_version("v1.1.0"):
            return "mgmt-br"
        else:
            return "harvester-br0"

    def create_data(self, name, namespace, vlan_id, bridge_name, mode="auto", cidr="", gateway=""):
        data = {
            "apiVersion": self.API_VERSION,
            "kind": self._KIND,
            "metadata": {
                "annotations": {
                    "network.harvesterhci.io/route": json.dumps({
                        "mode": mode,
                        "serverIPAddr": "",
                        "cidr": cidr,
                        "gateway": gateway
                    })
                },
                "name": name,
                "namespace": namespace
            },
            "spec": {
                "config": json.dumps({
                    "cniVersion": "0.3.1",
                    "name": name,
                    "type": "bridge",
                    "bridge": bridge_name,
                    "promiscMode": True,
                    "vlan": vlan_id,
                    "ipam": {}
                })
            }
        }
        return self._inject_data(data)

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=name, ns=namespace, NETWORK_API=self.API_VERSION)
        return self._get(path, raw=raw)

    def create(self, name, vlan_id, namespace=DEFAULT_NAMESPACE, *,
               cluster_network=None, mode="auto", cidr="", gateway="", raw=False):
        data = self.create_data(name, namespace, vlan_id, self._bridge_name(cluster_network),
                                mode=mode, cidr=cidr, gateway=gateway)
        path = self.PATH_fmt.format(uid="", ns=namespace, NETWORK_API=self.API_VERSION)
        return self._create(path, json=data, raw=raw)

    def update(self, *args, **kwargs):
        raise NotImplementedError("Update Network is not allowed")

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        path = self.PATH_fmt.format(uid=name, ns=namespace, NETWORK_API=self.API_VERSION)
        return self._delete(path, raw=raw)


class IPPoolManager(BaseManager):
    PATH_fmt = "{API_VERSION}/harvester/loadbalancer.harvesterhci.io.ippools{name}"
    API_VERSION = "v1"

    def create_data(self, name, ip_pool_subnet, ip_pool_start, ip_pool_end, network_id):
        return {
            "type": "loadbalancer.harvesterhci.io.ippool",
            "metadata": {
                "name": name
            },
            "spec": {
                "ranges": [{
                    "subnet": ip_pool_subnet,
                    "rangeStart": ip_pool_start,
                    "rangeEnd": ip_pool_end,
                    "gateway": "",
                    "type": "range" if ip_pool_start or ip_pool_end else "cidr"
                }],
                "selector": {
                    "network": network_id,
                    "scope": [{
                        "namespace": "*",
                        "project": "*",
                        "guestCluster": "*"
                    }]
                }
            }
        }

    def create(self, name, ip_pool_subnet, ip_pool_start, ip_pool_end, network_id, *, raw=False):
        data = self.create_data(name, ip_pool_subnet, ip_pool_start, ip_pool_end, network_id)
        path = self.PATH_fmt.format(name="", API_VERSION=self.API_VERSION)
        return self._create(path, json=data, raw=raw)

    def get(self, name="", *, raw=False):
        path = self.PATH_fmt.format(name=f"/{name}", API_VERSION=self.API_VERSION)
        return self._get(path, raw=raw)

    def delete(self, name, *, raw=False):
        path = self.PATH_fmt.format(name=f"/{name}", API_VERSION=self.API_VERSION)
        return self._delete(path, raw=raw)


class LoadBalancerManager(BaseManager):
    PATH_fmt = "{API_VERSION}/harvester/loadbalancer.harvesterhci.io.loadbalancers{uid}"
    API_VERSION = "v1"

    def get(self, uid="", *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{uid}", API_VERSION=self.API_VERSION)
        return self._get(path, raw=raw)

    def delete(self, uid, *, raw=False):
        path = self.PATH_fmt.format(uid=f"/{uid}", API_VERSION=self.API_VERSION)
        return self._delete(path, raw=raw)
