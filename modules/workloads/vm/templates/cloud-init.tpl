#cloud-config
ssh_pwauth: True
%{~ if password != null }
chpasswd:
  expire: False
  list:
    - ${default_user}:${password}
%{~ endif }
users:
  - name: ${default_user}
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    lock_passwd: false
%{~ if length(ssh_authorized_keys) > 0 }
    ssh_authorized_keys:
%{~ for key in ssh_authorized_keys }
      - ${key}
%{~ endfor }
%{~ endif }
