class UserSpec:
    _data = None

    def __init__(self, password, display_name="", description="",
                 change_pwd=False, enabled=True, permissions=None):
        self.password = password
        self.display_name = display_name
        self.change_pwd = change_pwd
        self.description = description
        self.enabled = enabled

    def to_dict(self, username):
        data = {
            "type": "user",
            "enabled": self.enabled,
            "username": username,
            "mustChangePassword": self.change_pwd,
            "password": self.password
        }

        if self.display_name:
            data['name'] = self.display_name
        if self.description:
            data['description'] = self.description

        return data

    @classmethod
    def from_dict(cls, data):
        obj = cls(**dict(
            display_name=data.get('name'),
            description=data.get('description'),
            change_pwd=data.get('mustChangePassword'),
            enabled=data.get('enabled'),
        ))
        obj._data = data
        return obj


class ChartSpec:
    def __init__(self, cluster_id, namespace, chart):
        self.cluster_id = cluster_id
        self.namespace = namespace
        self.chart = chart

    def to_dict(self):
        data = {
            "charts": [
                {
                    "chartName": self.chart,
                    "releaseName": self.chart,
                    "annotations": {
                        "catalog.cattle.io/ui-source-repo-type": "cluster",
                        "catalog.cattle.io/ui-source-repo": "rancher-charts"
                    },
                    "values": {
                        "global": {
                            "cattle": {
                                "systemDefaultRegistry": "",
                                "clusterId": self.cluster_id,
                                "rkePathPrefix": "",
                                "rkeWindowsPathPrefix": ""
                            },
                            "systemDefaultRegistry": ""
                        }
                    }
                }
            ],
            "noHooks": False,
            "timeout": "600s",
            "wait": True,
            "namespace": self.namespace,
            "disableOpenAPIValidation": False,
            "skipCRDs": False
        }

        return data


class LBServiceSpec:
    """ Data model for Load Balancer Service
    """
    def __init__(self, namespace: str, name: str, ipam: str, selector: dict):
        self.namespace = namespace
        self.name = name
        self.ipam = ipam    # dhcp or pool
        self.selector = selector

    def to_dict(self):
        data = {
            "type": "service",
            "metadata": {
                "namespace": self.namespace,
                "name": self.name,
                "annotations": {
                    "cloudprovider.harvesterhci.io/ipam": self.ipam
                }
            },
            "spec": {
                "type": "LoadBalancer",
                "sessionAffinity": None,
                "ports": [
                    {
                        "name": "http",
                        "port": 8080,
                        "protocol": "TCP",
                        "targetPort": 80
                    }
                ],
                "selector": self.selector
            }
        }

        return data
