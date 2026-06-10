#cloud-config
ssh_pwauth: true
chpasswd:
  list:
    - "ubuntu:${password}"
  expire: False
%{~ if length(ssh_authorized_keys) > 0 }

ssh_authorized_keys:
%{ for key in ssh_authorized_keys ~}
  - ${key}
%{ endfor ~}
%{~ endif }

packages:
  - qemu-guest-agent
  - curl
  - gnupg
  - lsb-release

write_files:
  - path: /etc/nginx/nginx.conf
    permissions: '0644'
    encoding: b64
    content: ${nginx_conf_b64}
  - path: /etc/nginx/certs/tls.crt
    permissions: '0644'
    encoding: b64
    content: ${tls_cert_b64}
  - path: /etc/nginx/certs/tls.key
    permissions: '0600'
    encoding: b64
    content: ${tls_key_b64}

runcmd:
  - systemctl enable --now qemu-guest-agent
  - |
    (
      until curl -s --connect-timeout 5 http://1.1.1.1 > /dev/null; do
        sleep 5
      done

      curl -fsSL https://nginx.org/keys/nginx_signing.key | gpg --dearmor > /usr/share/keyrings/nginx-archive-keyring.gpg
      echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] https://nginx.org/packages/ubuntu $(lsb_release -cs) nginx" > /etc/apt/sources.list.d/nginx.list
      apt-get update
      apt-get install -y nginx

      mkdir -p /etc/nginx/certs
      nginx -t && systemctl enable --now nginx
    ) >> /var/log/nginx-setup.log 2>&1 &
