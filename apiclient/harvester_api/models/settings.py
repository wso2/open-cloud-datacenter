from json import loads, dumps


class BaseSettingSpec:
    """ Base class for instance check and create"""
    _name = ""  # to point out the name of setting
    _default = False  # to point out to use default value

    def __init__(self, value=None):
        self.value = value or dict()

    def __repr__(self):
        return f"{self.__class__.__name__}({self.value})"

    @property
    def use_default(self):
        return self._default

    @use_default.setter
    def use_default(self, val):
        self._default = val

    def to_dict(self, data):
        return dict(value=dumps(self.value))

    @classmethod
    def from_dict(cls, data):
        for c in cls.__subclasses__():
            if c._name == data.get('metadata', {}).get('name'):
                return c.from_dict(data)
        return cls(data)


class BackupTargetSpec(BaseSettingSpec):
    _name = "backup-target"

    @property
    def type(self):
        return self.value.get('type')

    def clear(self):
        self.value = dict()

    @classmethod
    def from_dict(cls, data):
        value = loads(data.get('value', "{}"))
        return cls(value)

    @classmethod
    def S3(cls, bucket, region, access_id, access_secret, endpoint="", virtual_hosted=None):
        data = {
            "type": "s3",
            "endpoint": endpoint,
            "bucketName": bucket,
            "bucketRegion": region,
            "accessKeyId": access_id,
            "secretAccessKey": access_secret
        }
        if virtual_hosted is not None:
            data['virtualHostedStyle'] = virtual_hosted
        return cls(data)

    @classmethod
    def NFS(cls, endpoint):
        return cls(dict(type="nfs", endpoint=endpoint))


class KubeconfigDefaultTokenTTLSpec(BaseSettingSpec):
    _name = "kubeconfig-default-token-ttl-minutes"
    _default = 0

    @property
    def type(self):
        return self.value.get('type')

    def clear(self):
        self.value = self._default

    @classmethod
    def from_dict(cls, data):
        value = loads(data.get('value', "{}"))
        return cls(value)

    @classmethod
    def TTL(cls, minutes):
        return cls(minutes)


class StorageNetworkSpec(BaseSettingSpec):
    _name = "storage-network"

    @classmethod
    def disable(cls):
        obj = cls()
        obj.use_default = True
        return obj

    @classmethod
    def enable_with(cls, vlan_id, cluster_network, ip_range, *excludes):
        return cls({
            "vlan": vlan_id,
            "clusterNetwork": cluster_network,
            "range": ip_range,
            "exclude": excludes
        })

    @classmethod
    def from_dict(cls, data):
        value = loads(data.get('value', "{}"))
        return cls(value)

    def to_dict(self, data):
        if self.use_default or not self.value:
            return dict(value=None)
        return super().to_dict(data)


class OverCommitConfigSpec(BaseSettingSpec):
    _name = 'overcommit-config'

    @property
    def cpu(self):
        return self.value['cpu']

    @cpu.setter
    def cpu(self, val):
        self.value['cpu'] = val

    @property
    def memory(self):
        return self.value['memory']

    @memory.setter
    def memory(self, val):
        self.value['memory'] = val

    @property
    def storage(self):
        return self.value['storage']

    @storage.setter
    def storage(self, val):
        self.value['storage'] = val

    @classmethod
    def from_dict(cls, data):
        value = loads(data.get('value', '{}'))
        return cls(value)

    def to_dict(self, data):
        if self.use_default or not self.value:
            return dict(value=data['default'])
        return super().to_dict(data)
