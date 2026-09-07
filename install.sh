#!/usr/bin/env bash
# Downloads and installs the latest rmtunnel release for this box's
# architecture. Usage:
#   curl -fsSL https://raw.githubusercontent.com/rm-aliii/rmtunnel/main/install.sh | sudo bash
#
# Safe to re-run: it just overwrites the installed binary with whatever the
# latest release currently is.
set -euo pipefail

REPO="rm-aliii/rmtunnel"
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

mkdir -p "$CONF_DIR"
if [ ! -f "${CONF_DIR}/server.toml" ] && [ ! -f "${CONF_DIR}/client.toml" ]; then
  if curl -fsSL -o "${CONF_DIR}/server.toml.example" "https://raw.githubusercontent.com/${REPO}/main/examples/server.toml" 2>/dev/null &&
     curl -fsSL -o "${CONF_DIR}/client.toml.example" "https://raw.githubusercontent.com/${REPO}/main/examples/client.toml" 2>/dev/null; then
    log "example configs placed in ${CONF_DIR}/*.toml.example — copy one to server.toml or client.toml and edit it"
  fi
fi

echo
"${BIN_DIR}/rmtunnel" 2>&1 | head -1 || true
echo
log "next steps:"
echo "  1. edit ${CONF_DIR}/server.toml (or client.toml) — at minimum set 'token' and the disguise addresses"
echo "  2. run it directly to test:  rmtunnel server ${CONF_DIR}/server.toml"
echo "  3. or install the systemd service — see systemd/README.md in the repo"
echo "  4. want to know which config preset fits this box and link? run:"
echo "       rmtunnel bench server 0.0.0.0:9999 some-temp-token     (on one box)"
echo "       rmtunnel bench client <other-box-ip>:9999 some-temp-token   (on the other)"
