version: 2
ethernets:
  enp1s0:
    dhcp4: false
    addresses:
      - ${static_ip}/${subnet_prefix}
    routes:
      - to: default
        via: ${gateway}
    nameservers:
      addresses:
%{~ for dns in dns_servers }
        - ${dns}
%{~ endfor }
