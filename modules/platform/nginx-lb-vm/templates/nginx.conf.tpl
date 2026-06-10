worker_processes auto;
pid /run/nginx.pid;
include /etc/nginx/modules-enabled/*.conf;

events {
    worker_connections 4096;
    multi_accept on;
    use epoll;
}

stream {

    upstream rancher-registration {
        least_conn;
%{ for ip in node_ips ~}
        server ${ip}:9345 max_fails=3 fail_timeout=30s;
%{ endfor ~}
    }

    upstream rancher-k8s-api {
        least_conn;
%{ for ip in node_ips ~}
        server ${ip}:6443 max_fails=3 fail_timeout=30s;
%{ endfor ~}
    }

    server {
        listen 9345;
        proxy_pass rancher-registration;
        proxy_timeout 3600s;
        proxy_connect_timeout 10s;
    }

    server {
        listen 6443;
        proxy_pass rancher-k8s-api;
        proxy_timeout 3600s;
        proxy_connect_timeout 10s;
    }

}

http {

    map $http_upgrade $connection_upgrade {
        default upgrade;
        '' close;
    }

    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 75s;
    keepalive_requests 10000;
    types_hash_max_size 2048;
    server_tokens off;

    client_max_body_size 200M;
    client_body_buffer_size 512k;

    proxy_buffer_size 128k;
    proxy_buffers 8 256k;
    proxy_busy_buffers_size 256k;
    proxy_temp_file_write_size 256k;

    proxy_read_timeout 300s;
    proxy_connect_timeout 10s;
    proxy_send_timeout 300s;

    open_file_cache max=20000 inactive=30s;
    open_file_cache_valid 60s;
    open_file_cache_min_uses 2;
    open_file_cache_errors on;

    ssl_session_cache shared:SSL:50m;
    ssl_session_timeout 1h;
    ssl_session_tickets off;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers on;

    gzip on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;
    gzip_min_length 1024;
    gzip_proxied any;

    server {
        listen 80;
        server_name ${rancher_hostname};
        return 301 https://$host$request_uri;
    }

    upstream rancher-nodeport {
        least_conn;
%{ for ip in node_ips ~}
        server ${ip}:443 max_fails=3 fail_timeout=30s;
%{ endfor ~}
        keepalive 64;
    }

    server {

        listen 443 ssl http2;
        server_name ${rancher_hostname};

        ssl_certificate     /etc/nginx/certs/tls.crt;
        ssl_certificate_key /etc/nginx/certs/tls.key;

        location ^~ /k8s/clusters/ {
            proxy_pass https://rancher-nodeport;
            proxy_http_version 1.1;
            proxy_set_header Host              $host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Upgrade    $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_buffering off;
            proxy_request_buffering off;
            proxy_read_timeout 3600s;
            proxy_send_timeout 3600s;
        }

        location ~* ^/(v3/subscribe|api/websocket) {
            proxy_pass https://rancher-nodeport;
            proxy_http_version 1.1;
            proxy_set_header Host              $host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Upgrade    $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_buffering off;
            proxy_read_timeout 3600s;
            proxy_send_timeout 3600s;
        }

        location ~* ^/(assets/|dashboard/assets/) {
            proxy_pass https://rancher-nodeport;
            proxy_http_version 1.1;
            proxy_set_header Host              $host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_buffering on;
            expires 1h;
            add_header Cache-Control "public";
            etag on;
        }

        location / {
            proxy_pass https://rancher-nodeport;
            proxy_http_version 1.1;
            proxy_set_header Host              $host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Upgrade    $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_buffering on;
            proxy_read_timeout 300s;
        }

    }

}
