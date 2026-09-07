# systemd services

On the **server** (Iran) box:

```bash
sudo cp rmtunnel-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now rmtunnel-server
sudo journalctl -u rmtunnel-server -f   # watch logs
```

On the **client** (Kharej) box:

```bash
sudo cp rmtunnel-client.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now rmtunnel-client
sudo journalctl -u rmtunnel-client -f
```

Both expect the binary at `/usr/local/bin/rmtunnel` and the config at
`/etc/rmtunnel/server.toml` or `/etc/rmtunnel/client.toml` — exactly where
`install.sh` puts them.

`rmtunnel-server.service` grants `CAP_NET_BIND_SERVICE` so it can listen on
port 443 (for the `wss` disguise) without running as root — everything else
about the process stays unprivileged.
