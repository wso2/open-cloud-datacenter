from .virtualmachines import VMSpec, VMSpec160, VMSpec180


class TemplateSpec(VMSpec):
    def to_dict(self, name, namespace, hostname=""):
        vd = super().to_dict(name, namespace, "")
        metadata = vd.pop('metadata')
        spec = vd.pop('spec')

        # we can't modify name/namespace/description in template
        metadata.pop('namespace'), metadata.pop('name')
        metadata['annotations'].pop('field.cattle.io/description')
        # hostname are not injected in the time
        spec['template']['spec'].pop('hostname')

        return {
            "metadata": {
                "generateName": f"{name}-",
                "labels": {
                    "template.harvesterhci.io/templateID": name
                },
                "namespace": namespace
            },
            "spec": {
                "templateId": f"{namespace}/{name}",
                "vm": dict(metadata=metadata, spec=spec)
            }
        }

    @classmethod
    def from_dict(cls, data):
        if "VirtualMachineTemplateVersion" != data.get('kind'):
            raise ValueError("Only support data comes from 'VirtualMachineTemplateVersion'")

        vd = data['spec']['vm']

        vd['type'] = "kubevirt.io.virtualmachine"
        vd['spec']['template']['spec']['hostname'] = ""
        return super().from_dict(vd)


class TemplateSpec160(TemplateSpec, VMSpec160):
    pass


class TemplateSpec180(TemplateSpec, VMSpec180):
    pass
