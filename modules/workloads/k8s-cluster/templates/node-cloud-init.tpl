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
%{~ if length(ssh_authorized_keys) > 0 }
ssh_authorized_keys:
%{~ for key in ssh_authorized_keys }
  - ${key}
%{~ endfor }
%{~ endif }
