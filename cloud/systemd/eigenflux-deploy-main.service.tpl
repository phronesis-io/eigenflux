[Unit]
Description=Deploy EigenFlux from the latest origin/main
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=root
Group=root
WorkingDirectory=/
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bin
ExecStart=/usr/local/sbin/eigenflux-deploy-main
StandardOutput=journal
StandardError=journal
SyslogIdentifier=eigenflux-deploy-main
NoNewPrivileges=true
PrivateTmp=true
TimeoutStartSec=infinity
