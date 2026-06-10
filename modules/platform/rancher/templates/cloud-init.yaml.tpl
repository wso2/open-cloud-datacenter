#cloud-config
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
%{~ if tls_source == "secret" && !use_nginx_lb }

write_files:
  - path: /tmp/rancher-tls.crt
    permissions: '0600'
    encoding: b64
    content: ${tls_cert_b64}
  - path: /tmp/rancher-tls.key
    permissions: '0600'
    encoding: b64
    content: ${tls_key_b64}
%{~ endif }

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
      until curl -s --connect-timeout 5 http://1.1.1.1 > /dev/null; do
        sleep 5
      done
%{~ if is_control_plane }

      curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=${rke2_version} sh -
%{~ else }

      curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=${rke2_version} INSTALL_RKE2_TYPE=agent sh -
%{~ endif }
      mkdir -p /etc/rancher/rke2
%{~ if is_init_node }

      cat > /etc/rancher/rke2/config.yaml <<'RKCFG'
      token: ${rke2_cluster_token}
      cluster-init: true
      tls-san:
        - ${lb_ip}
    RKCFG
%{~ else }

%{~ if is_control_plane }
      cat > /etc/rancher/rke2/config.yaml <<'RKCFG'
      token: ${rke2_cluster_token}
      server: https://${join_ip}:9345
      tls-san:
        - ${lb_ip}
    RKCFG
%{~ else }
      cat > /etc/rancher/rke2/config.yaml <<'RKCFG'
      token: ${rke2_cluster_token}
      server: https://${join_ip}:9345
    RKCFG
%{~ endif }
%{~ endif }

%{~ if is_control_plane }
      systemctl enable rke2-server
      systemctl start rke2-server
%{~ else }
      systemctl enable rke2-agent
      systemctl start rke2-agent
%{~ endif }
%{~ if is_init_node }

      until [ -f /etc/rancher/rke2/rke2.yaml ]; do sleep 5; done
      ln -sf /var/lib/rancher/rke2/bin/kubectl /usr/local/bin/kubectl
      export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
      mkdir -p /home/ubuntu/.kube
      cp /etc/rancher/rke2/rke2.yaml /home/ubuntu/.kube/config
      chown -R ubuntu:ubuntu /home/ubuntu/.kube
      chmod 600 /home/ubuntu/.kube/config

      until [ "$(kubectl get nodes --no-headers 2>/dev/null | grep -c ' Ready')" -ge "${total_node_count}" ]; do
        sleep 15
      done

      kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.1/cert-manager.yaml
      kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=600s
%{~ if tls_source == "secret" && !use_nginx_lb }

      kubectl create namespace cattle-system --dry-run=client -o yaml | kubectl apply -f -
      kubectl create secret tls tls-rancher-ingress \
        --cert=/tmp/rancher-tls.crt --key=/tmp/rancher-tls.key \
        -n cattle-system --dry-run=client -o yaml | kubectl apply -f -
      rm -f /tmp/rancher-tls.crt /tmp/rancher-tls.key
%{~ endif }

      curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
%{~ if use_rancher_prime }
      helm repo add rancher-prime https://charts.rancher.com/server-charts/prime
%{~ else }
      helm repo add rancher-latest https://releases.rancher.com/server-charts/latest
%{~ endif }
      helm repo update

      for i in $(seq 1 10); do
%{~ if use_rancher_prime }
        helm upgrade --install rancher rancher-prime/rancher \
%{~ else }
        helm upgrade --install rancher rancher-latest/rancher \
%{~ endif }
          --namespace cattle-system --create-namespace \
%{~ if rancher_version != "" }
          --version ${rancher_version} \
%{~ endif }
          --set hostname=${cluster_dns} \
          --set bootstrapPassword=${rancher_password} \
          --set replicas=${cp_node_count} \
          --set global.cattle.psp.enabled=false \
%{~ if !use_nginx_lb }
          --set ingress.tls.source=${tls_source} \
%{~ endif }
          --wait --timeout 15m && break
        sleep 30
      done
%{~ endif }
    ) >> /var/log/rancher-install.log 2>&1 &
