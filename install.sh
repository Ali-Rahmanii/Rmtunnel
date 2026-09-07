#!/usr/bin/env bash
# Downloads and installs the latest rmtunnel release for this box's
# architecture. Usage:
#   curl -fsSL https://raw.githubusercontent.com/Ali-Rahmanii/rmtunnel/main/install.sh | sudo bash
#
# Safe to re-run: it just overwrites the installed binary with whatever the
# latest release currently is.
set -euo pipefail

REPO="Ali-Rahmanii/rmtunnel"
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/rmtunnel"

log() { printf '\033[1;32m==>\033[0m %s\n' "$1"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "run this as root (sudo bash install.sh)"

case "$(uname -s)" in
  Linux) ;;
  *) die "rmtunnel's pre-built binaries are Linux-only — see README.md to build from source for your OS" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

URL="https://github.com/${REPO}/releases/latest/download/rmtunnel-linux-${ARCH}"
log "downloading rmtunnel for linux/${ARCH}..."
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
if ! curl -fsSL -o "$TMP" "$URL"; then
  die "download failed — is there a published release at https://github.com/${REPO}/releases ?"
fi
chmod +x "$TMP"
install -m 0755 "$TMP" "${BIN_DIR}/rmtunnel"
log "installed to ${BIN_DIR}/rmtunnel"

mkdir -p "$CONF_DIR" "$CONF_DIR/tunnels/server" "$CONF_DIR/tunnels/client"
if [ ! -f "${CONF_DIR}/server.toml.example" ]; then
  curl -fsSL -o "${CONF_DIR}/server.toml.example" "https://raw.githubusercontent.com/${REPO}/main/examples/server.toml" 2>/dev/null || true
  curl -fsSL -o "${CONF_DIR}/client.toml.example" "https://raw.githubusercontent.com/${REPO}/main/examples/client.toml" 2>/dev/null || true
fi

echo
echo
log "installed. run this next:"
echo
echo "    sudo rmtunnel"
echo
echo "that opens an interactive menu that builds the config (Iran/server or"
echo "Kharej/client side), tunes the OS, benchmarks the link, and installs the"
echo "systemd service for you. example configs were also placed in"
echo "${CONF_DIR}/*.toml.example if you'd rather write one by hand."
