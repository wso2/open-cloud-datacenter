#cloud-config
ssh_pwauth: true
chpasswd:
  list:
    - ubuntu:${password}
  expire: False

ssh_authorized_keys:
  - ${ssh_public_key}

packages:
  - qemu-guest-agent
  - curl
  - iptables

runcmd:
  - systemctl enable --now qemu-guest-agent
  - sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT=.*/GRUB_CMDLINE_LINUX_DEFAULT="console=ttyS0,115200n8 console=tty0"/' /etc/default/grub
  - update-grub

  - until ping -c 1 8.8.8.8; do sleep 5; done

  - |
    (
      echo "--- Starting RKE2 and Rancher Deployment ---"
      
      # 1. Install RKE2
      curl -sfL https://get.rke2.io | sh -
      mkdir -p /etc/rancher/rke2

      cat <<EOF > /etc/rancher/rke2/config.yaml
      token: static-bootstrap-token-123
      $( [ "${node_index}" != "0" ] && echo "server: https://${lb_ip}:9345" )
    EOF

      systemctl enable rke2-server.service
      systemctl start rke2-server.service

      # 2. IMMEDIATE kubectl download
      echo "Downloading kubectl..."
      curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
      install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl

      # 3. Wait for Config & API
      echo "Waiting for rke2.yaml..."
      until [ -f /etc/rancher/rke2/rke2.yaml ]; do sleep 10; done
      export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
      
      # 4. Setup User Context
      mkdir -p /home/ubuntu/.kube
      cp /etc/rancher/rke2/rke2.yaml /home/ubuntu/.kube/config
      chown -R ubuntu:ubuntu /home/ubuntu/.kube
      chmod 600 /home/ubuntu/.kube/config

      # 5. Wait for Node Readiness
      echo "Waiting for nodes to be Ready..."
      until /usr/local/bin/kubectl get nodes | grep -q "Ready"; do sleep 10; done

      if [ "${node_index}" -eq "0" ]; then
        echo "Initializing Rancher..."
        curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
        /usr/local/bin/helm repo add rancher-stable https://releases.rancher.com/server-charts/stable
        /usr/local/bin/helm repo update
        
        # 6. Two-Stage Ingress Wait (Corrected for DaemonSet)
        echo "Waiting for Ingress DaemonSet existence..."
        until /usr/local/bin/kubectl get ds rke2-ingress-nginx-controller -n kube-system; do sleep 10; done
        
        echo "Waiting for Ingress DaemonSet pods to be Ready..."
        until [ "$(/usr/local/bin/kubectl get ds rke2-ingress-nginx-controller -n kube-system -o jsonpath='{.status.numberReady}')" = "$(/usr/local/bin/kubectl get ds rke2-ingress-nginx-controller -n kube-system -o jsonpath='{.status.desiredNumberScheduled}')" ]; do
          echo "Waiting for Ingress pods... (Ready vs Desired)"
          sleep 10
        done
        
        # Settle time for the Admission Webhook Service
        sleep 30 
        
        # 7. Cert-Manager
        /usr/local/bin/kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.1/cert-manager.yaml
        /usr/local/bin/kubectl wait --for=condition=Available --timeout=600s deployment/cert-manager-webhook -n cert-manager
        
        # 8. Rancher Installation Loop
        for i in {1..10}; do
          echo "Rancher install attempt $i..."
          /usr/local/bin/helm upgrade --install rancher rancher-stable/rancher \
            --namespace cattle-system \
            --create-namespace \
            --set hostname=${cluster_dns} \
            --set bootstrapPassword=${rancher_password} \
            --set replicas=${node_count} \
            --wait && break || sleep 30
        done
      fi
      echo "--- Deployment Script Finished ---"
    ) > /var/log/rancher-install.log 2>&1 &
    