#cloud-config
ssh_pwauth: True
%{~ if node_password != null }
chpasswd:
  expire: False
  list:
    - ${ssh_user}:${node_password}
%{~ endif }
package_update: true
packages:
  - qemu-guest-agent
  - nfs-common
  - net-tools
  - ipvsadm
%{~ if enable_gvisor }
  - curl
  - gnupg
  - ca-certificates
%{~ endif }
write_files:
  - path: /etc/systemd/timesyncd.conf
    content: |
      [Time]
      NTP=${ntp_server}
    owner: root:root
    permissions: '0644'
  - path: /etc/sysctl.d/99-inotify.conf
    content: |
      fs.inotify.max_user_watches=524288
      fs.inotify.max_user_instances=8192
      fs.inotify.max_queued_events=65536
    owner: root:root
    permissions: '0644'
  - path: /etc/modules-load.d/ipvs.conf
    content: |
      ip_vs
      ip_vs_rr
      ip_vs_wrr
      ip_vs_sh
      nf_conntrack
    owner: root:root
    permissions: '0644'
%{~ if enable_storage_netplan }
  - path: /etc/netplan/60-storage-network.yaml
    owner: root:root
    permissions: '0600'
    content: |
      network:
        version: 2
        ethernets:
          enp2s0:
            dhcp4: true
            dhcp4-overrides:
              use-routes: false
              route-metric: 500
%{~ endif }
%{~ if enable_gvisor }
  # RKE2 renders this into /var/lib/rancher/rke2/agent/etc/containerd/config.toml.
  # Written before Rancher's system-agent installs RKE2; extends the base
  # template so registries, CNI paths and runc settings are preserved.
  - path: /var/lib/rancher/rke2/agent/etc/containerd/${gvisor.containerd_tmpl}
    owner: root:root
    permissions: '0644'
    content: |
      {{ template "base" . }}

      [plugins.${gvisor.cri_plugin}.containerd.runtimes.${gvisor.runtime_handler}]
        runtime_type = "io.containerd.runsc.v1"

      [plugins.${gvisor.cri_plugin}.containerd.runtimes.${gvisor.runtime_handler}.options]
        TypeUrl = "io.containerd.runsc.v1.options"
        ConfigPath = "/etc/containerd/runsc.toml"
  # Installs the pinned gVisor release from the signed apt repo and writes
  # /etc/containerd/runsc.toml. Kept on disk for manual re-runs (upgrades/CVE
  # patches); replacement nodes always get the release pinned in Terraform.
  - path: /usr/local/sbin/install-gvisor.sh
    owner: root:root
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      # Usage: install-gvisor.sh [release]
      set -euo pipefail
      GVISOR_RELEASE="$${1:-${gvisor.release}}"
      KEYRING=/usr/share/keyrings/gvisor-archive-keyring.gpg

      curl -fsSL --retry 5 "${gvisor.apt_key_url}" | gpg --dearmor --yes -o "$KEYRING"
      echo "deb [arch=$(dpkg --print-architecture) signed-by=$${KEYRING}] ${gvisor.apt_repo_url} $${GVISOR_RELEASE} main" \
        > /etc/apt/sources.list.d/gvisor.list
      apt-get update
      apt-mark unhold runsc >/dev/null 2>&1 || true
      DEBIAN_FRONTEND=noninteractive apt-get install -y runsc
      apt-mark hold runsc

      RUNSC="$(command -v runsc)"
      command -v containerd-shim-runsc-v1 >/dev/null
      install -d -m 0755 /etc/containerd
      printf 'binary_name = "%s"\n' "$RUNSC" > /etc/containerd/runsc.toml
      # Quoted delimiter: runsc_config values are written literally, never shell-expanded.
      cat >> /etc/containerd/runsc.toml <<'EOF'
      [runsc_config]
%{~ for k, v in gvisor.runsc_config }
        ${k} = "${v}"
%{~ endfor }
      EOF
      "$RUNSC" --version
%{~ endif }
runcmd:
  - - systemctl
    - enable
    - --now
    - qemu-guest-agent.service
  - modprobe ip_vs
  - modprobe ip_vs_rr
  - modprobe ip_vs_wrr
  - modprobe ip_vs_sh
  - modprobe nf_conntrack
  - sysctl --system
  - systemctl restart systemd-timesyncd
  - timedatectl set-ntp false
  - timedatectl set-ntp true
%{~ if enable_gvisor }
  - /usr/local/sbin/install-gvisor.sh ${gvisor.release}
%{~ endif }
%{~ if enable_storage_netplan }
  - netplan generate
  - netplan apply
%{~ endif }
%{~ if length(ssh_authorized_keys) > 0 }
ssh_authorized_keys:
%{~ for key in ssh_authorized_keys }
  - ${key}
%{~ endfor }
%{~ endif }
