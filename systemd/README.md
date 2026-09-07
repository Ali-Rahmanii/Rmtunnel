# systemd services

These are **template units** — one file covers any number of named tunnels.
`%i` becomes whatever name you give the tunnel.

The interactive menu (`sudo rmtunnel` → build a tunnel → "install as a
systemd service?") does this for you automatically and does **not** need
these files installed — it writes its own self-contained unit per tunnel
under `/etc/systemd/system/rmtunnel-<role>@<name>.service`. Use the files
here only if you're setting a tunnel up by hand.

On the **server** (Iran) box:

```bash
sudo cp rmtunnel-server@.service /etc/systemd/system/
sudo systemctl daemon-reload
# put the config at /etc/rmtunnel/tunnels/server/<name>.toml, then:
sudo systemctl enable --now rmtunnel-server@<name>
sudo journalctl -u rmtunnel-server@<name> -f
```

On the **client** (Kharej) box:

```bash
sudo cp rmtunnel-client@.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now rmtunnel-client@<name>
sudo journalctl -u rmtunnel-client@<name> -f
```

Running more than one tunnel on the same box is just enabling more
instances — `rmtunnel-server@web`, `rmtunnel-server@wireguard`, whatever
names fit — each with its own config file and its own independent service.

`rmtunnel-server@.service` grants `CAP_NET_BIND_SERVICE` so it can listen on
port 443 (for the `wss` disguise) without running as root — everything else
about the process stays unprivileged.

Managing tunnels this way from now on? The menu's "Manage Tunnels" screen
(`sudo rmtunnel` → option 3) lists, edits, starts/stops/restarts, tails logs
for, and deletes any tunnel under `/etc/rmtunnel/tunnels/`, regardless of
whether it was created by the wizard or by hand — it only needs the config
file and the matching service instance to exist.
