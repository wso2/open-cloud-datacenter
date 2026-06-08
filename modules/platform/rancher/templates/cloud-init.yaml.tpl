#cloud-config
ssh_pwauth: true
chpasswd:
  list:
    - "ubuntu:${password}"
  expire: False

ssh_authorized_keys:
  - ${ssh_public_key}
%{~ if primary_dns != "" }

bootcmd:
  - mkdir -p /etc/systemd/resolved.conf.d
  - "echo '[Resolve]' > /etc/systemd/resolved.conf.d/primary-dns.conf"
  - "echo 'DNS=${primary_dns}' >> /etc/systemd/resolved.conf.d/primary-dns.conf"
  - systemctl restart systemd-resolved
%{~ endif }

packages:
  - qemu-guest-agent
  - curl
  - avahi-daemon
  - avahi-utils
%{~ if tls_source == "secret" }

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
  - |
    (
      until curl -s --connect-timeout 5 http://1.1.1.1 > /dev/null; do
        sleep 5
      done

      curl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=${rke2_version} sh -
      mkdir -p /etc/rancher/rke2
%{~ if node_index == 0 }

      cat > /etc/rancher/rke2/config.yaml <<'RKCFG'
      token: ${rke2_cluster_token}
      cluster-init: true
      tls-san:
        - ${lb_ip}
    RKCFG
%{~ else }

      # Discover node-0's IP via mDNS — avahi-daemon on node-0 advertises
      # its hostname (${vm_name}-0.local) on the local VLAN automatically.
      JOIN_IP=""
      until JOIN_IP=$(avahi-resolve-host-name -4 ${vm_name}-0.local 2>/dev/null | awk '{print $2}') && [ -n "$JOIN_IP" ]; do
        sleep 5
      done

      cat > /etc/rancher/rke2/config.yaml <<RKCFG
      token: ${rke2_cluster_token}
      server: https://$JOIN_IP:9345
      tls-san:
        - ${lb_ip}
    RKCFG
%{~ endif }

      systemctl enable rke2-server
      systemctl start rke2-server

      until [ -f /etc/rancher/rke2/rke2.yaml ]; do sleep 5; done
      ln -sf /var/lib/rancher/rke2/bin/kubectl /usr/local/bin/kubectl
      export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
      mkdir -p /home/ubuntu/.kube
      cp /etc/rancher/rke2/rke2.yaml /home/ubuntu/.kube/config
      chown -R ubuntu:ubuntu /home/ubuntu/.kube
      chmod 600 /home/ubuntu/.kube/config
%{~ if node_index == 0 }

      until [ "$(kubectl get nodes --no-headers 2>/dev/null | grep -c ' Ready')" -ge "${node_count}" ]; do
        sleep 15
      done
%{~ if use_metallb }

      kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/${metallb_version}/config/manifests/metallb-native.yaml
      kubectl -n metallb-system rollout status deployment/controller --timeout=300s
      sleep 15

      kubectl apply -f - <<'METALLB'
      apiVersion: metallb.io/v1beta1
      kind: IPAddressPool
      metadata:
        name: rancher-pool
        namespace: metallb-system
      spec:
        addresses:
          - ${metallb_ip}/32
    METALLB

      kubectl apply -f - <<'L2ADV'
      apiVersion: metallb.io/v1beta1
      kind: L2Advertisement
      metadata:
        name: rancher-l2
        namespace: metallb-system
      spec:
        ipAddressPools:
          - rancher-pool
    L2ADV

      until kubectl -n kube-system get ds rke2-ingress-nginx-controller &>/dev/null; do sleep 5; done
      kubectl -n kube-system rollout status daemonset/rke2-ingress-nginx-controller --timeout=300s

      kubectl apply -f - <<'INGRESSLB'
      apiVersion: v1
      kind: Service
      metadata:
        name: rke2-ingress-lb
        namespace: kube-system
        annotations:
          metallb.universe.tf/loadBalancerIPs: ${metallb_ip}
      spec:
        type: LoadBalancer
        selector:
          app.kubernetes.io/name: rke2-ingress-nginx
        ports:
          - name: http
            port: 80
            targetPort: 80
          - name: https
            port: 443
            targetPort: 443
    INGRESSLB
%{~ endif }

      kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.1/cert-manager.yaml
      kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=600s
%{~ if tls_source == "secret" }

      kubectl create namespace cattle-system --dry-run=client -o yaml | kubectl apply -f -
      kubectl create secret tls tls-rancher-ingress \
        --cert=/tmp/rancher-tls.crt --key=/tmp/rancher-tls.key \
        -n cattle-system --dry-run=client -o yaml | kubectl apply -f -
      rm -f /tmp/rancher-tls.crt /tmp/rancher-tls.key
%{~ endif }

      curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
      helm repo add rancher-latest https://releases.rancher.com/server-charts/latest
      helm repo update

      for i in $(seq 1 10); do
        helm upgrade --install rancher rancher-latest/rancher \
          --namespace cattle-system --create-namespace \
%{~ if rancher_version != "" }
          --version ${rancher_version} \
%{~ endif }
          --set hostname=${cluster_dns} \
          --set bootstrapPassword=${rancher_password} \
          --set replicas=${node_count} \
          --set global.cattle.psp.enabled=false \
          --set ingress.tls.source=${tls_source} \
          --wait --timeout 15m && break
        sleep 30
      done
%{~ endif }
    ) >> /var/log/rancher-install.log 2>&1 &
