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
%{~ if rke2_config_join_b64 != "" }

  # The same config but joining instead of initialising. This node is the
  # designated cluster-init node; see the bootstrap script for which one wins.
  - path: /etc/rancher/rke2/config.join.yaml
    permissions: '0600'
    owner: 'root:root'
    encoding: b64
    content: ${rke2_config_join_b64}
%{~ endif }

  - path: /usr/local/bin/rke2-bootstrap.sh
    permissions: '0755'
    owner: 'root:root'
    content: |
      #!/bin/bash
      # Installs RKE2, starts it, and (on a server) publishes an admin
      # kubeconfig. Safe to re-run at any point: an already-running service
      # skips straight to the kubeconfig step rather than exiting, so a run
      # interrupted between the two still finishes. Re-run after a failure:
      #   sudo /usr/local/bin/rke2-bootstrap.sh
      set -uo pipefail

      SERVICE=%{ if is_server }rke2-server%{ else }rke2-agent%{ endif }
      JOIN_URL="https://${kube_api_host}:9345"
      INSTALL_TYPE=%{ if is_server }%{ else }agent%{ endif }
      VERSION=${rke2_version}

      INSTALLER=/var/lib/rancher/rke2-install.sh
      ATTEMPTS=10
      DELAY=15

      # How long the unit may take to report ready. Generous: a joining server
      # pulls images and syncs etcd before systemd sees it as active.
      START_TIMEOUT=600

      log() { echo "[$(date -Is)] rke2-bootstrap: $*"; }
      die() { log "FATAL: $*"; exit 1; }

      if systemctl is-active --quiet "$SERVICE"; then
        log "$SERVICE is already active; skipping install and start"
      else
        mkdir -p /var/lib/rancher

%{~ if rke2_config_join_b64 != "" }
        # cluster-init vs join. This node's config says cluster-init, which is
        # correct exactly once: at the very first bootstrap. If the VM is later
        # replaced — changing its static IP rebuilds it with a fresh boot disk —
        # booting with cluster-init again would start a SECOND empty datastore
        # beside the surviving servers and split etcd irrecoverably.
        #
        # The supervisor on $JOIN_URL only answers once a server is actually
        # serving. On a first bootstrap the load balancer has no healthy backend
        # yet, so the probe fails and cluster-init stands. On a rebuild the
        # surviving servers answer, and this node joins them instead.
        if [ -f /etc/rancher/rke2/config.join.yaml ]; then
          joined=no
          for attempt in 1 2 3 4 5 6; do
            if curl -sfk --connect-timeout 5 --max-time 15 "$JOIN_URL/cacerts" >/dev/null 2>&1; then
              joined=yes
              break
            fi
            log "no cluster answering at $JOIN_URL (probe $attempt/6)"
            sleep 5
          done

          if [ "$joined" = yes ]; then
            log "an existing cluster answered at $JOIN_URL: joining it, NOT running cluster-init"
            mv /etc/rancher/rke2/config.join.yaml /etc/rancher/rke2/config.yaml
          else
            log "nothing answered at $JOIN_URL: treating this as the first bootstrap and running cluster-init"
            rm -f /etc/rancher/rke2/config.join.yaml
          fi
        fi
%{~ endif }

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

        # Started with --no-block and polled, not with `enable --now`. The RKE2
        # units are Type=notify with TimeoutStartSec=0, so systemctl blocks until
        # the service reports ready and waits forever if it never does — e.g. a
        # joining server that cannot reach its supervisor. That would hang here
        # and never reach any later deadline.
        systemctl enable "$SERVICE" || die "failed to enable $SERVICE"
        systemctl start --no-block "$SERVICE" || die "failed to start $SERVICE"

        waited=0
        until systemctl is-active --quiet "$SERVICE"; do
          if systemctl is-failed --quiet "$SERVICE"; then
            die "$SERVICE entered a failed state; see 'journalctl -u $SERVICE'"
          fi
          if [ "$waited" -ge "$START_TIMEOUT" ]; then
            die "$SERVICE still not active after $${START_TIMEOUT}s; see 'journalctl -u $SERVICE'"
          fi
          sleep 5
          waited=$((waited + 5))
        done
        log "$SERVICE active after $${waited}s"
      fi

      # Outside the branch above: ln -sf is idempotent, and a resumed run should
      # restore the symlink if it is missing.
      ln -sf /var/lib/rancher/rke2/bin/kubectl /usr/local/bin/kubectl
%{~ if is_server }

      # Publish an admin kubeconfig for the operator to copy off the node. The
      # server address is rewritten to the external endpoint so the file works
      # as-is from outside the VM:
      #   scp ubuntu@${node_ip}:.kube/config rke2.kubeconfig
      #
      # Reached whether or not the service was already running, so a run
      # interrupted between starting RKE2 and writing this file finishes on the
      # next invocation. Bounded: an unbounded wait here is indistinguishable
      # from a hung node.
      waited=0
      until [ -f /etc/rancher/rke2/rke2.yaml ]; do
        if [ "$waited" -ge 900 ]; then
          die "rke2.yaml did not appear within 900s; check 'journalctl -u $SERVICE'"
        fi
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
