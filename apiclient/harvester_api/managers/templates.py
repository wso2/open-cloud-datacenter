from harvester_api.models.templates import TemplateSpec, TemplateSpec160, TemplateSpec180
from .base import DEFAULT_NAMESPACE, BaseManager


class TemplateManager(BaseManager):
    # get, create, delete
    PATH_fmt = "apis/{{API_VERSION}}/namespaces/{ns}/virtualmachinetemplates/{uid}"
    VER_PATH_fmt = "apis/{{API_VERSION}}/namespaces/{ns}/virtualmachinetemplateversions/{uid}"
    _KIND = "VirtualMachineTemplate"
    _VER_KIND = "VirtualMachineTemplateVersion"

    Spec = TemplateSpec

    def create_data(self, name, namespace, description):
        data = {
            "apiVersion": "{API_VERSION}",
            "kind": self._KIND,
            "metadata": {
                "name": name,
                "namespace": namespace
            },
            "spec": {
                "description": description
            }
        }
        return self._inject_data(data)

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name, ns=namespace), raw=raw)

    def get_version(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._get(self.VER_PATH_fmt.format(uid=name, ns=namespace), raw=raw)

    def create(self, name, namespace=DEFAULT_NAMESPACE, description="", *, raw=False):
        data = self.create_data(name, namespace, description)
        path = self.PATH_fmt.format(ns=namespace, uid="")
        return self._create(path, json=data, raw=raw)

    def create_version(self, name, template_spec, namespace=DEFAULT_NAMESPACE, *, raw=False):
        if isinstance(template_spec, self.Spec):
            template_spec = template_spec.to_dict(name, namespace)
        template_spec.update(apiVersion="{API_VERSION}", kind=self._VER_KIND)
        data = self._inject_data(template_spec)
        path = self.VER_PATH_fmt.format(uid="", ns=namespace)
        return self._create(path, json=data, raw=raw)

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name, ns=namespace), raw=raw)

    def delete_version(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._delete(self.VER_PATH_fmt.format(uid=name, ns=namespace), raw=raw)


class TemplateManager160(TemplateManager):
    support_to = "v1.6.0"
    Spec = TemplateSpec160


class TemplateManager180(TemplateManager):
    support_to = "v1.8.0rc1"
    Spec = TemplateSpec180
