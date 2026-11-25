#cloud-config
# Installs RKE2 and joins this node to the cluster. Nothing else is deployed
# here — Rancher itself is a Helm release managed by platform/rancher-install.
#
# The RKE2 config is rendered by Terraform with yamlencode() and delivered as a
# base64 write_files entry, so there are no shell heredocs to mis-indent.
ssh_pwauth: true
chpasswd:
  list:
    - ${jsonencode("ubuntu:${password}")}
  expire: False

%{~ if length(ssh_public_keys) > 0 }
ssh_authorized_keys:
%{~ for key in ssh_public_keys }
  - ${jsonencode(key)}
%{~ endfor }
%{~ endif }

packages:
  - qemu-guest-agent
  - curl

write_files:
  - path: /etc/rancher/rke2/config.yaml
    permissions: '0600'
    owner: 'root:root'
    encoding: b64
    content: ${rke2_config_b64}

  - path: /usr/local/bin/rke2-bootstrap.sh
    permissions: '0755'
    owner: 'root:root'
    content: |
      #!/bin/bash
      # Installs RKE2 and starts it. Idempotent and safe to re-run: it exits
      # early once the service is active, and the installer itself tolerates
      # being run again over a partial install. Re-run after a failure with:
      #   sudo /usr/local/bin/rke2-bootstrap.sh
      set -uo pipefail

      SERVICE=%{ if is_server }rke2-server%{ else }rke2-agent%{ endif }
      INSTALL_TYPE=%{ if is_server }%{ else }agent%{ endif }
      VERSION=${rke2_version}

      INSTALLER=/var/lib/rancher/rke2-install.sh
      ATTEMPTS=10
      DELAY=15

      log() { echo "[$(date -Is)] rke2-bootstrap: $*"; }
      die() { log "FATAL: $*"; exit 1; }

      if systemctl is-active --quiet "$SERVICE"; then
        log "$SERVICE is already active; nothing to do"
        exit 0
      fi

      mkdir -p /var/lib/rancher

      # Download to a file rather than piping into sh. A piped curl failure is
      # invisible: sh reads empty stdin and exits 0, so the install silently
      # does nothing and only surfaces later as a missing unit.
      n=0
      while [ "$n" -lt "$ATTEMPTS" ]; do
        n=$((n + 1))
        if curl -sfL --connect-timeout 10 --max-time 180 \
             -o "$INSTALLER.tmp" https://get.rke2.io && [ -s "$INSTALLER.tmp" ]; then
          mv "$INSTALLER.tmp" "$INSTALLER"
          log "installer downloaded on attempt $n"
          break
        fi
        rm -f "$INSTALLER.tmp"
        log "installer download failed (attempt $n/$ATTEMPTS); retrying in $DELAY s"
        sleep "$DELAY"
      done

      [ -s "$INSTALLER" ] || die "could not download the RKE2 installer after $ATTEMPTS attempts"

      if [ -n "$INSTALL_TYPE" ]; then
        INSTALL_RKE2_VERSION="$VERSION" INSTALL_RKE2_TYPE="$INSTALL_TYPE" sh "$INSTALLER" \
          || die "RKE2 installer exited non-zero"
      else
        INSTALL_RKE2_VERSION="$VERSION" sh "$INSTALLER" \
          || die "RKE2 installer exited non-zero"
      fi

      # Confirm the install actually produced the unit before starting it.
      systemctl cat "$SERVICE" >/dev/null 2>&1 \
        || die "$SERVICE.service is missing after a successful installer run"

      ln -sf /var/lib/rancher/rke2/bin/kubectl /usr/local/bin/kubectl

      systemctl enable --now "$SERVICE" || die "failed to start $SERVICE"
      log "$SERVICE started"
%{~ if is_server }

      # Publish an admin kubeconfig for the operator to copy off the node. The
      # server address is rewritten to the external endpoint so the file works
      # as-is from outside the VM:
      #   scp ubuntu@${node_ip}:.kube/config rke2.kubeconfig
      #
      # Bounded: an unbounded wait here is indistinguishable from a hung node.
      waited=0
      until [ -f /etc/rancher/rke2/rke2.yaml ]; do
        [ "$waited" -ge 900 ] && die "rke2.yaml did not appear within 900s; check 'journalctl -u $SERVICE'"
        sleep 5
        waited=$((waited + 5))
      done

      mkdir -p /home/ubuntu/.kube
      sed 's|https://127.0.0.1:6443|https://${kube_api_host}:6443|' \
        /etc/rancher/rke2/rke2.yaml > /home/ubuntu/.kube/config
      chown -R ubuntu:ubuntu /home/ubuntu/.kube
      chmod 600 /home/ubuntu/.kube/config
      log "admin kubeconfig published to /home/ubuntu/.kube/config"
%{~ endif }
      log "bootstrap complete"

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
  # Backgrounded so cloud-init does not block on the install. If it fails the
  # script exits non-zero and says so in the log; re-run it by hand to recover.
  - "nohup /usr/local/bin/rke2-bootstrap.sh >> /var/log/rke2-bootstrap.log 2>&1 &"
