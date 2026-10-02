from collections.abc import Mapping

from .base import BaseManager, merge_dict


class ClusterNetworkManager(BaseManager):
    PATH_fmt = "apis/network.{{API_VERSION}}/clusternetworks/{uid}"
    CONFIG_fmt = "apis/network.{{API_VERSION}}/vlanconfigs/{uid}"
    _default_bond_mode = "active-backup"

    def create_data(self, name, description="", labels=None, annotations=None):
        data = {
            "apiVersion": "network.{API_VERSION}",
            "kind": "ClusterNetwork",
            "metadata": {
                "annotations": {
                    "field.cattle.io/description": description
                },
                "labels": labels or dict(),
                "name": name
            }

        }

        if annotations is not None:
            data['metadata']['annotations'].update(annotations)

        return self._inject_data(data)

    def create_config_data(self, name, clusternetwork, *nics,
                           bond_mode=None, hostname=None, miimon=None, mtu=None):
        data = {
            "apiVersion": "network.{API_VERSION}",
            "kind": "VlanConfig",
            "metadata": {
                "name": name
            },
            "spec": {
                "clusterNetwork": clusternetwork,
                "uplink": {
                    "bondOptions": {
                        "mode": bond_mode or self._default_bond_mode,
                    },
                    "linkAttributes": {
                        "TxQLen": -1
                    },
                    "nics": nics
                }
            }
        }

        if hostname is not None:
            data['spec']['nodeSelector'] = {"kubernetes.io/hostname": hostname}

        if miimon is not None:
            data['spec']['uplink']['bondOptions']['miimon'] = miimon

        if mtu is not None:
            data['spec']['uplink']["linkAttributes"] = {
                "mtu": mtu
            }

        return self._inject_data(data)

    def get(self, name="", raw=False):
        path = self.PATH_fmt.format(uid=name)
        return self._get(path, raw=raw)

    def create(self, name, description="", labels=None, annotations=None, *, raw=False):
        data = self.create_data(name, description, labels, annotations)
        path = self.PATH_fmt.format(uid="")
        return self._create(path, json=data, raw=raw)

    def update(self, name, data, *, raw=False, as_json=True, **kwargs):
        path = self.PATH_fmt.format(uid=name)
        if isinstance(data, Mapping) and as_json:
            _, node = self.get(name)
            data = merge_dict(data, node)
        return self._update(path, data, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, *, raw=False):
        path = self.PATH_fmt.format(uid=name)
        return self._delete(path, raw=raw)

    def get_config(self, name="", raw=False):
        path = self.CONFIG_fmt.format(uid=name)
        return self._get(path, raw=raw)

    def create_config(self, name, clusternetwork, *nics,
                      bond_mode=None, hostname=None, miimon=None, mtu=None, raw=False):
        data = self.create_config_data(name, clusternetwork, *nics, bond_mode=bond_mode,
                                       hostname=hostname, miimon=miimon, mtu=mtu)
        path = self.CONFIG_fmt.format(uid=name)
        return self._create(path, json=data, raw=raw)

    def update_config(self, name, data, *, raw=False, as_json=True, **kwargs):
        path = self.CONFIG_fmt.format(uid=name)
        if isinstance(data, Mapping) and as_json:
            _, node = self.get_config(name)
            data = merge_dict(data, node)
        return self._update(path, data, raw=raw, as_json=as_json, **kwargs)

    def delete_config(self, name, *, raw=False):
        path = self.CONFIG_fmt.format(uid=name)
        return self._delete(path, raw=raw)
