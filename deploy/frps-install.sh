#!/usr/bin/env bash
#
# Installs frps on a Linux server so the ScienceOne desktop bridge can be
# reached at a stable address.
#
#   sudo ./frps-install.sh <token> <remotePort>
#
# It downloads frp, installs frps as a systemd service on port 7000, and allows
# exactly one remote port through. Terminating TLS is left to you — see
# frps.md next to this script.
set -euo pipefail

TOKEN="${1:-}"
REMOTE_PORT="${2:-}"
if [ -z "$TOKEN" ] || [ -z "$REMOTE_PORT" ]; then
	echo "usage: $0 <token> <remotePort>" >&2
	echo "  <token>       shared secret; pass the same value to the client as -frp-token" >&2
	echo "  <remotePort>  the port on this server that will carry the tunnel" >&2
	exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "run me as root" >&2
	exit 1
fi

VERSION=0.71.0
case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*)
		echo "unsupported architecture: $(uname -m)" >&2
		exit 1
		;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> downloading frp ${VERSION} (${ARCH})"
curl -fsSL -o "$TMP/frp.tar.gz" \
	"https://github.com/fatedier/frp/releases/download/v${VERSION}/frp_${VERSION}_linux_${ARCH}.tar.gz"
tar -xzf "$TMP/frp.tar.gz" -C "$TMP"
install -m 0755 "$TMP/frp_${VERSION}_linux_${ARCH}/frps" /usr/local/bin/frps

echo "==> writing /etc/frp/frps.toml"
mkdir -p /etc/frp
cat >/etc/frp/frps.toml <<EOF
bindPort = 7000
auth.token = "${TOKEN}"

# Only the bridge's own port is reachable through this server, so a leaked token
# cannot be turned into a port forwarder for anything else.
allowPorts = [{ start = ${REMOTE_PORT}, end = ${REMOTE_PORT} }]
EOF
chmod 600 /etc/frp/frps.toml

echo "==> installing the systemd unit"
cat >/etc/systemd/system/frps.service <<'EOF'
[Unit]
Description=frp server
After=network.target

[Service]
ExecStart=/usr/local/bin/frps -c /etc/frp/frps.toml
Restart=always
RestartSec=5s
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now frps
sleep 1
systemctl --no-pager --lines=0 status frps || true

cat <<EOF

done. next:

  1. open 7000 (frp control) and ${REMOTE_PORT} in your firewall
  2. terminate TLS in front of 127.0.0.1:${REMOTE_PORT} — see frps.md
  3. start the bridge with:

     scienceone-desktop.exe -tunnel frp -frp-server <this host> -frp-port 7000 \\
       -frp-token ${TOKEN} -frp-remote ${REMOTE_PORT} -public-url https://<your domain>
EOF
