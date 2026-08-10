[Service]
WorkingDirectory=/var/lib/eigenflux-deployer/current/source
EnvironmentFile=/etc/eigenflux/runtime.env
ExecStartPre=
ExecStart=
ExecStartPre=/usr/bin/test -x /var/lib/eigenflux-deployer/current/bin/%i
ExecStart=/var/lib/eigenflux-deployer/current/bin/%i
