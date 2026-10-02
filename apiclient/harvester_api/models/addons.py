from json import loads, dumps

from yaml import safe_load, safe_dump


class _BaseConfig:
    def __init__(self, data):
        self._val = data
        self.limits = data['resources']['limits']
        self.requests = data['resources']['requests']

    def sync(self):
        """ To sync attributes into dictionary"""


class BaseAddonSpec:
    """ Base class for instance check and create """
    _name = ""  # to point out the name of addon
    _id = ""
    _enable = False  # the addon is enable or not

    def __init__(self, value=None, enabled=False):
        self.value = value or dict()
        self.enable = enabled

    def __repr__(self):
        return f"{self.__class__.__name__}({self.value})"

    @property
    def enable(self):
        return self._enable

    @enable.setter
    def enable(self, val):
        self._enable = val

    def to_dict(self, data):
        return dict(spec=dict(enabled=self.enable, valuesContent=safe_dump(self.value)))

    @classmethod
    def from_dict(cls, data):
        for c in cls.__subclasses__():
            if c._name == data.get('metadata', {}).get('name'):
                return c.from_dict(data)
        spec = data.get('spec', {})
        value = safe_load(spec.get('valuesContent', ""))
        enable = spec.get('enabled', False)
        return cls(value, enable)


class MonitoringAddonSpec(BaseAddonSpec):
    _name = "rancher-monitoring"
    _id = "cattle-monitoring-system/rancher-monitoring"

    class _PrometheusExporter(_BaseConfig):
        pass

    class _Grafana(_BaseConfig):
        pass

    class _Prometheus(_BaseConfig):
        def __init__(self, data):
            super().__init__(data)
            self.retention = dict(value=data['retention'], size=data['retentionSize'])
            self.scrape_interval = data['scrapeInterval']
            self.evaluation_interval = data['evaluationInterval']
            self.url = data['externalUrl']  # shortcut for check the link

        def sync(self):
            self._val["retention"] = self.retention["value"]
            self._val["retentionSize"] = self.retention["size"]
            self._val["scrapeInterval"] = self.scrape_interval
            self._val["evaluationInterval"] = self.evaluation_interval

    class _AlertManager(_BaseConfig):
        def __init__(self, data):
            super().__init__(data['alertmanagerSpec'])
            self._val = data
            self.enable = data['enabled']
            self.retention = data['alertmanagerSpec']['retention']
            self.url = data['alertmanagerSpec']['externalUrl']  # shortcut for check the link

        def sync(self):
            self._val['retention'] = self.retention
            self._val['enabled'] = self.enable

    def __init__(self, value=None, enabled=False):
        super().__init__(value, enabled)
        self.prometheus = self._Prometheus(value['prometheus']['prometheusSpec'])
        self.grafana = self._Grafana(value['grafana'])
        self.prometheus_exporter = self._PrometheusExporter(value['prometheus-node-exporter'])
        self.alertmanager = self._AlertManager(value['alertmanager'])

    def to_dict(self, data):
        self.prometheus.sync()
        self.alertmanager.sync()
        return super().to_dict(data)


class LoggingAddonSpec(BaseAddonSpec):
    _name = "rancher-logging"
    _id = "cattle-logging-system/rancher-logging"

    class _Fluentbit(_BaseConfig):
        pass

    class _Fluentd(_BaseConfig):
        pass

    def __init__(self, value=None, enabled=False):
        super().__init__(value, enabled)
        self.fluentbit = self._Fluentbit(value.get("fluentbit", {}))
        self.fluentd = self._Fluentd(value.get("fluentd", {}))


class VMImportControllerAddonSpec(BaseAddonSpec):
    _name = "vm-import-controller"
    _id = "harvester-system/vm-import-controller"

    def __init__(self, value=None, enabled=False):
        super().__init__(value, enabled)
        self.limits = value.get('resources', {}).get('limits', {})
        self.requests = value.get('resources', {}).get('requests', {})

    def to_dict(self, data):
        return dict(spec=dict(enabled=self.enable, valuesContent=dumps(self.value)))

    @classmethod
    def from_dict(cls, data):
        spec = data.get('spec', {})
        value = loads(spec.get('valuesContent', "{}"))
        enable = spec.get('enabled', False)
        return cls(value, enable)
