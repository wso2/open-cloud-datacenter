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
