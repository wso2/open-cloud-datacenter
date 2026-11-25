#cloud-config
# Installs RKE2 and joins this node to the cluster. Nothing else is deployed
# here — Rancher itself is a Helm release managed by platform/rancher-install.
#
# The RKE2 config is rendered by Terraform with yamlencode() and delivered as a
# base64 write_files entry, so there are no shell heredocs to mis-indent.
ssh_pwauth: true
chpasswd:
  list:
    - "ubuntu:${password}"
  expire: False

ssh_authorized_keys:
  - ${ssh_public_key}

packages:
  - qemu-guest-agent
  - curl

write_files:
  - path: /etc/rancher/rke2/config.yaml
    permissions: '0600'
    owner: 'root:root'
    encoding: b64
    content: ${rke2_config_b64}

runcmd:
  - systemctl enable --now qemu-guest-agent
  - sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT=.*/GRUB_CMDLINE_LINUX_DEFAULT="console=ttyS0,115200n8 console=tty0"/' /etc/default/grub
  - update-grub
%{~ if length(dns_servers) > 0 }
  - mkdir -p /etc/systemd/resolved.conf.d
  - "echo '[Resolve]' > /etc/systemd/resolved.conf.d/dns.conf"
  - "echo 'DNS=${join(" ", dns_servers)}' >> /etc/systemd/resolved.conf.d/dns.conf"
  - systemctl restart systemd-resolved
%{~ endif }
  - |
    (
      # Wait for outbound connectivity before pulling the installer.
      until curl -s --connect-timeout 5 https://get.rke2.io > /dev/null; do
        sleep 5
      done

%{~ if is_server }
      curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=${rke2_version} sh -
      systemctl enable --now rke2-server
%{~ else }
      curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=${rke2_version} INSTALL_RKE2_TYPE=agent sh -
      systemctl enable --now rke2-agent
%{~ endif }

      ln -sf /var/lib/rancher/rke2/bin/kubectl /usr/local/bin/kubectl
%{~ if is_server }

      # Publish an admin kubeconfig for the operator to copy off the node. The
      # server address is rewritten to the external endpoint so the file works
      # as-is from outside the VM:
      #   scp ubuntu@${node_ip}:.kube/config rke2.kubeconfig
      until [ -f /etc/rancher/rke2/rke2.yaml ]; do sleep 5; done
      mkdir -p /home/ubuntu/.kube
      sed 's|https://127.0.0.1:6443|https://${kube_api_host}:6443|' \
        /etc/rancher/rke2/rke2.yaml > /home/ubuntu/.kube/config
      chown -R ubuntu:ubuntu /home/ubuntu/.kube
      chmod 600 /home/ubuntu/.kube/config
%{~ endif }
    ) >> /var/log/rke2-bootstrap.log 2>&1 &
