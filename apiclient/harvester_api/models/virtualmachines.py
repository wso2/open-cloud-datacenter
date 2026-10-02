from copy import deepcopy
from json import dumps, loads

import yaml

from .base import MGMT_NETID, DEFAULT_STORAGE_CLS
from .volumes import VolumeSpec, VolumeSpec180


class VMSpec:
    # ref: https://kubevirt.io/api-reference/master/definitions.html#_v1_virtualmachineinstancespec
    # defaults
    cpu_sockets = cpu_threads = 1
    run_strategy = "RerunOnFailure"
    hostname = machine_type = ""
    usbtablet = True
    _data = None
    # Since v1.4.0
    eviction_strategy = "LiveMigrateIfPossible"
    cpu_pinning = False
    tpm_enabled = False
    tpm_persistent = False

    # TODOs:
    # node selector(affinity)
    # userdata/networkdata enhancement
    # SSH Key

    def __init__(self, cpu_cores, memory, description="", reserved_mem="", os_type="",
                 mgmt_network=True, guest_agent=True):

        self._cloudinit_vol = {
            "disk": {
                "name": "cloudinitdisk",
                "disk": dict(bus="virtio")
            },
            "volume": {
                "name": "cloudinitdisk",
                "cloudInitNoCloud": {
                    "networkData": "",
                    "userData": "#cloud-config\npackage_update: true"
                }
            }
        }

        # default
        self.volumes, self.networks = [], []
        self._firmwares, self._features = dict(), dict()
        self.acpi = True
        # initial data
        self.cpu_cores = cpu_cores
        self.cpu_model = None
        self.memory = memory
        self.mgmt_network = mgmt_network
        self.guest_agent = guest_agent
        self.description = description
        self.reserved_mem = reserved_mem
        self.os_type = os_type

    @property
    def mgmt_network(self):
        return bool([n for n in self.networks if n['network'].get('pod', None) is not None])

    @mgmt_network.setter
    def mgmt_network(self, enable):
        if enable:
            if not self.mgmt_network:
                self.add_network("default", MGMT_NETID)
        else:
            self.networks = [n for n in self.networks if n['network'].get('pod', None) is None]

    @property
    def acpi(self):
        return self._features['acpi'].get('enabled')

    @acpi.setter
    def acpi(self, enable):
        self._features['acpi'] = dict(enabled=enable)

    @property
    def efi_boot(self):
        return 'efi' in self._firmwares.get('bootloader', dict())

    @efi_boot.setter
    def efi_boot(self, enable):
        if enable:
            self._features['smm'] = dict(enabled=False)
            self._firmwares['bootloader'] = dict(efi=dict(secureBoot=False))
        else:
            self._features.pop('smm', None)
            self._firmwares.pop('bootloader', None)

    @property
    def secure_boot(self):
        return self.efi_boot and self._firmwares['bootloader']['efi'].get('secureBoot', False)

    @secure_boot.setter
    def secure_boot(self, enable):
        if enable:
            self.efi_boot = True
            self._firmwares['bootloader']['efi']['secureBoot'] = True
            self._features['smm']['enabled'] = True
        else:
            self.efi_boot = False
            self.efi_boot = True

    @property
    def guest_agent(self):
        return 'qemu-guest-agent.service' in self.user_data

    @guest_agent.setter
    def guest_agent(self, enable):
        cmd = 'systemctl enable --now qemu-guest-agent.service'
        userdata = yaml.safe_load(self.user_data) or dict()
        pkgs = userdata.get('packages', [])
        runcmds = [' '.join(c) for c in userdata.get('runcmd', [])]
        if enable:
            if 'qemu-guest-agent' not in pkgs:
                userdata.setdefault('packages', []).append('qemu-guest-agent')

            if cmd not in runcmds:
                userdata.setdefault('runcmd', []).append(cmd.split())
        else:
            userdata['packages'] = [p for p in pkgs if p != 'qemu-guest-agent']
            userdata['runcmd'] = [c.split() for c in runcmds if c != cmd]

        self.user_data = "#cloud-config\n" + yaml.dump(userdata)

    @property
    def user_data(self):
        return self._cloudinit_vol['volume']['cloudInitNoCloud'].get('userData', "")

    @user_data.setter
    def user_data(self, val):
        if isinstance(val, str):
            if val.split('\n', 1)[0] != "#cloud-config":
                val = f"#cloud-config\n{val}"
        self._cloudinit_vol['volume']['cloudInitNoCloud']['userData'] = val

    @property
    def network_data(self):
        return self._cloudinit_vol['volume']['cloudInitNoCloud'].get('networkData', "")

    @network_data.setter
    def network_data(self, val):
        self._cloudinit_vol['volume']['cloudInitNoCloud']['networkData'] = val

    def add_cd_rom(self, name, image_id, size=10, bus="SATA", image_uid=None):
        vol_spec = VolumeSpec(size, storage_cls=f"longhorn-{image_id.split('/')[1]}",
                              annotations={"harvesterhci.io/imageId": image_id})

        vol = {
            "claim": vol_spec,
            "disk": {
                "name": name,
                "cdrom": dict(bus=bus),
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "persistentVolumeClaim": dict(claimName="")
            }
        }

        self.volumes.append(vol)
        return vol

    def add_image(self, name, image_id, size=10, bus="virtio", type="disk", image_uid=None):
        vol_spec = VolumeSpec(size, storage_cls=f"longhorn-{image_id.split('/')[1]}",
                              annotations={"harvesterhci.io/imageId": image_id})

        vol = {
            "claim": vol_spec,
            "disk": {
                type: dict(bus=bus),
                "name": name,
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "persistentVolumeClaim": dict(claimName="")
            }
        }
        self.volumes.append(vol)
        return vol

    def add_container(self, name, docker_image, bus="virtio", type="disk"):
        vol = {
            "disk": {
                type: dict(bus=bus),
                "name": name,
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "containerDisk": dict(image=docker_image)
            }
        }

        self.volumes.append(vol)
        return vol

    def add_volume(self, name, size, type="disk", bus="virtio", storage_cls=DEFAULT_STORAGE_CLS):
        vol_spec = VolumeSpec(size, storage_cls)
        vol = {
            "claim": vol_spec,
            "disk": {
                type: dict(bus=bus),
                "name": name,
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "persistentVolumeClaim": dict(claimName="")
            }
        }

        self.volumes.append(vol)
        return vol

    def add_existing_volume(self, name, volume_name, type="disk"):
        vol = {
            "disk": {
                type: dict(bus="virtio"),
                "name": name,
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "persistentVolumeClaim": dict(claimName=volume_name)
            }
        }

        self.volumes.append(vol)
        return vol

    def add_network(self, name, net_uid, model="virtio", mac_addr=None):
        net = {
            "iface": dict(model=model, name=name),
            "network": dict(name=name)
        }

        if net_uid is MGMT_NETID:
            net['iface']["masquerade"] = dict()
            net['network']['pod'] = dict()
        else:
            net['iface']["bridge"] = dict()
            net['network']['multus'] = dict(networkName=net_uid)

        if mac_addr is not None:
            net['iface']['macAddress'] = mac_addr

        self.networks.append(net)
        return net

    def _update_bootorder(self):
        its = enumerate([v['disk'] for v in self.volumes if 'disk' in v], 1)
        for idx, disk in its:
            disk['bootOrder'] = idx

    def _update_volume_spec(self, name, namespace):
        volumes = []
        for v in deepcopy(self.volumes):
            if 'claim' in v:
                disk_name = v['volume']['name']
                v['claim'] = v['claim'].to_dict(f"{name}-{disk_name}", namespace)
                v['volume']['persistentVolumeClaim']['claimName'] = f"{name}-{disk_name}"
            volumes.append(v)
        return volumes

    def to_dict(self, name, namespace, hostname=""):
        self._update_bootorder()
        volumes = self._update_volume_spec(name, namespace) + [self._cloudinit_vol]
        mem = f"{self.memory}Gi" if isinstance(self.memory, int) else self.memory

        machine = dict(type=self.machine_type)
        cpu = dict(cores=self.cpu_cores, sockets=self.cpu_sockets, thread=self.cpu_threads)
        if self.cpu_model is not None:
            cpu['model'] = self.cpu_model
        resources = dict(cpu=self.cpu_cores, memory=mem)

        volume_claims = dumps([v['claim'] for v in volumes if 'claim' in v])

        data = {
            "type": "kubevirt.io.virtualmachine",
            "metadata": {
                "namespace": namespace,
                "name": name,
                "annotations": {
                    "harvesterhci.io/volumeClaimTemplates": volume_claims,
                    "field.cattle.io/description": self.description
                },
                "labels": {
                    "harvesterhci.io/creator": "harvester",
                    "harvesterhci.io/os": self.os_type
                }
            },
            "spec": {
                "runStrategy": self.run_strategy,
                "template": {
                    "metadata": {
                        "annotations": {
                            "harvesterhci.io/sshNames": '[]'
                        },
                        "labels": {
                            "harvesterhci.io/vmName": name
                        }
                    },
                    "spec": {
                        "evictionStrategy": self.eviction_strategy,
                        "hostname": hostname or self.hostname or name,
                        "networks": [n['network'] for n in self.networks],
                        "volumes": [v['volume'] for v in volumes if 'volume' in v],
                        "domain": {
                            "machine": machine,
                            "cpu": cpu,
                            "resources": dict(limits=resources),
                            "features": self._features,
                            "firmware": self._firmwares,
                            "devices": {
                                "interfaces": [n['iface'] for n in self.networks],
                                "disks": [v['disk'] for v in volumes if 'disk' in v]
                            },
                        }
                    }
                }
            }
        }

        if self.usbtablet:
            inputs = [dict(bus="usb", name="tablet", type="tablet")]
            data['spec']['template']['spec']['domain']['devices']['inputs'] = inputs

        if self.reserved_mem:
            r_mem = self.reserved_mem
            r_mem = f"{r_mem}Mi" if isinstance(r_mem, (int, float)) else r_mem
            data['metadata']['annotations']["harvesterhci.io/reservedMemory"] = r_mem

        if self.cpu_pinning:
            data['spec']['template']['spec']['domain']['cpu']['dedicatedCpuPlacement'] = True

        # Error handling for only enabling tpm_persistent case
        if self.tpm_persistent:
            self.tpm_enabled = True
        if self.tpm_enabled:
            tpm_payload = {}
            if self.tpm_persistent:
                tpm_payload["persistent"] = True
            data['spec']['template']['spec']['domain']['devices']['tpm'] = tpm_payload

        if self._data:
            self._data['metadata'].update(data['metadata'])
            self._data['spec'].update(data['spec'])
            return deepcopy(self._data)

        return deepcopy(data)

    @classmethod
    def from_dict(cls, data):
        if data['type'] != 'kubevirt.io.virtualmachine':
            raise ValueError("Only support extract data comes from 'kubevirt.io.virtualmachine'")

        data = deepcopy(data)
        spec, metadata = data.get('spec', {}), data.get('metadata', {})
        vm_spec = spec['template']['spec']

        run_strategy = spec['runStrategy']
        os_type = metadata.get('labels', {}).get("harvesterhci.io/os", "")
        desc = metadata['annotations'].get("field.cattle.io/description", "")
        reserved_mem = metadata['annotations'].get("harvesterhci.io/reservedMemory", "")
        vol_claims = {v['metadata']['name']: VolumeSpec.from_dict(v) for v in loads(
                        metadata['annotations'].get("harvesterhci.io/volumeClaimTemplates", "[]"))}

        hostname = vm_spec['hostname']
        eviction_strategy = vm_spec['evictionStrategy']
        networks = vm_spec['networks']
        volumes = vm_spec['volumes']
        machine = vm_spec['domain']['machine']
        cpu = vm_spec['domain']['cpu']
        mem = vm_spec['domain']['resources']['limits']['memory']
        features = vm_spec['domain'].get('features', {})
        firmware = vm_spec['domain'].get('firmware', {})
        devices = vm_spec['domain']['devices']
        is_pinned = vm_spec['domain']['cpu'].get("dedicatedCpuPlacement")

        obj = cls(cpu['cores'], mem, desc, reserved_mem, os_type)
        obj.cpu_sockets, obj.cpu_threads = cpu.get('sockets', 1), cpu.get('threads', 1)
        obj.cpu_model = cpu.get('model')
        obj.run_strategy = run_strategy
        obj.eviction_strategy = eviction_strategy
        obj.hostname = hostname
        obj.machine_type = machine.get('type', "")
        obj.usbtablet = devices.get('inputs') and bool(devices['inputs'][0])

        obj.metadata = metadata

        obj._features = features
        obj._firmwares = firmware
        obj.cpu_pinning = is_pinned
        obj._cloudinit_vol = dict(disk=devices['disks'][-1], volume=volumes[-1])
        obj.networks = [dict(iface=i, network=n) for i, n in zip(devices['interfaces'], networks)]
        obj.volumes = [dict(disk=d, volume=v) for d, v in zip(devices['disks'][:-1], volumes[:-1])]
        for v in obj.volumes:
            if "persistentVolumeClaim" in v['volume']:
                claim = v['volume']['persistentVolumeClaim']['claimName']
                if vol_claims.get(claim):
                    v['claim'] = vol_claims[claim]

        if "tpm" in devices:
            obj.tpm_enabled = True
            obj.tpm_persistent = devices['tpm'].get('persistent', False)
        else:
            obj.tpm_enabled = False
            obj.tpm_persistent = False

        obj._data = data
        return obj


class VMSpec160(VMSpec):
    # ref: https://docs.harvesterhci.io/v1.6/vm/cpu-memory-hotplug
    _cpu_mem_hotplug = False
    _custom_max_cpu = _custom_max_mem = _hotplug_ratio = 0  # Init the value

    def set_cpu_mem_hotplug(self, enable, custom_max_cpu=0, custom_max_mem=0, hotplug_ratio=4):
        status_change = self._cpu_mem_hotplug != enable
        # Don't need to update the spec if the status isn't changed, just return directly
        if not status_change:
            return

        self._cpu_mem_hotplug = enable
        if enable:
            self._custom_max_cpu = custom_max_cpu
            self._custom_max_mem = custom_max_mem
            self._hotplug_ratio = hotplug_ratio
            # The behavior after enable the function
            # Move cpu_cores value to cpu_sockets, and set cpu_cores = 1
            self.cpu_sockets = self.cpu_cores
            self.cpu_cores = 1
        else:
            self._custom_max_cpu = self._custom_max_mem = self._hotplug_ratio = 0
            self.cpu_cores = self.cpu_sockets
            self.cpu_sockets = 1

    def to_dict(self, name, namespace, hostname=""):
        data = super().to_dict(name, namespace, hostname)

        if self._cpu_mem_hotplug:
            memory = int(str(self.memory).replace("Gi", ""))

            if self._custom_max_cpu == 0:
                max_cpu = int(self.cpu_sockets) * int(self._hotplug_ratio)
            else:
                max_cpu = self._custom_max_cpu
            if self._custom_max_mem == 0:
                max_mem = memory * int(self._hotplug_ratio)
            else:
                max_mem = self._custom_max_mem

            data['metadata']['annotations'][
                'harvesterhci.io/enableCPUAndMemoryHotplug'] = 'true'
            domain = data['spec']['template']['spec']['domain']
            domain['cpu']['cores'] = self.cpu_cores
            domain['cpu']['sockets'] = self.cpu_sockets
            domain['cpu']['maxSockets'] = int(max_cpu)
            domain['resources'] = {
                'limits': {
                    'cpu': f'{max_cpu}',
                    'memory': f'{max_mem}Gi'
                }
            }
            domain['memory'] = {
                'guest': f'{memory}Gi',
                'maxGuest': f'{max_mem}Gi'
            }
        else:
            memory = int(str(self.memory).replace("Gi", ""))
            data['metadata']['annotations'].pop(
                'harvesterhci.io/enableCPUAndMemoryHotplug', None)
            domain = data['spec']['template']['spec']['domain']
            domain['cpu']['cores'] = self.cpu_cores
            domain['cpu']['sockets'] = self.cpu_sockets
            domain['resources'] = {
                'limits': {
                    'cpu': f'{self.cpu_cores}',
                    'memory': f'{memory}Gi'
                }
            }
            domain['memory'] = {
                'guest': f'{memory}Gi'
            }

        return data

    @classmethod
    def from_dict(cls, data):
        obj = super().from_dict(data)
        is_cpu_mem_hotplug = data['metadata']['annotations'].get(
            'harvesterhci.io/enableCPUAndMemoryHotplug', 'false') == 'true'
        obj._cpu_mem_hotplug = is_cpu_mem_hotplug

        if obj._cpu_mem_hotplug:
            domain = data['spec']['template']['spec']['domain']
            # Get resources limit values
            max_cpu = int(domain['resources']['limits']['cpu'])
            max_mem = int(domain['resources']['limits']['memory'].replace('Gi', ''))
            mem = int(domain['memory']['guest'].replace('Gi', ''))
            # Use the ratio to determine
            if max_mem / mem == max_cpu / obj.cpu_sockets:
                obj._hotplug_ratio = int(max_cpu / obj.cpu_sockets)
            # Use the custom value to set
            else:
                obj._hotplug_ratio = 4  # Set default value if the ratio isn't correct
            obj._custom_max_cpu = max_cpu
            obj._custom_max_mem = max_mem
        else:
            obj._custom_max_cpu = obj._custom_max_mem = obj._hotplug_ratio = 0  # Init the value

        return obj


class VMSpec180(VMSpec160):
    # ref: https://github.com/harvester/harvester/pull/9392
    # After v1.8.0, the `longhorn-{image_name}` storage classes are no longer auto-created.
    # For image-based volumes, must use lh-{image_uid} storage class.

    def add_cd_rom(self, name, image_id, size=10, bus="SATA", image_uid=None):
        vol_spec = VolumeSpec180(size, storage_cls=None,
                                 annotations={"harvesterhci.io/imageId": image_id})

        vol = {
            "claim": vol_spec,
            "image_id": image_id,
            "image_uid": image_uid,  # Store for _update_volume_spec
            "disk": {
                "name": name,
                "cdrom": dict(bus=bus),
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "persistentVolumeClaim": dict(claimName="")
            }
        }

        self.volumes.append(vol)
        return vol

    def add_image(self, name, image_id, size=10, bus="virtio", type="disk", image_uid=None):
        vol_spec = VolumeSpec180(size, storage_cls=f"lh-{image_uid}",
                                 annotations={"harvesterhci.io/imageId": image_id})

        vol = {
            "claim": vol_spec,
            "image_id": image_id,
            "image_uid": image_uid,  # Store for _update_volume_spec
            "disk": {
                type: dict(bus=bus),
                "name": name,
                "bootOrder": 1
            },
            "volume": {
                "name": name,
                "persistentVolumeClaim": dict(claimName="")
            }
        }
        self.volumes.append(vol)
        return vol
