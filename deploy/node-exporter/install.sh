#!/usr/bin/env bash
# Installs Node Exporter as a systemd service on a monitored server.
# Usage (as root):
#   MONITORING_HOST_IP=10.0.0.12 ./install.sh          listen on :9100, ufw opened to that host only
#   NODE_EXPORTER_LISTEN=10.0.0.11:9100 ./install.sh   listen on the WireGuard address only
#   NODE_EXPORTER_LISTEN=127.0.0.1:9100 ./install.sh   localhost only (scraped through an SSH tunnel)
# Idempotent: re-running upgrades the binary and rewrites the unit.
set -euo pipefail

VER="${NODE_EXPORTER_VERSION:-1.9.1}"
LISTEN="${NODE_EXPORTER_LISTEN:-:9100}"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

if [[ $EUID -ne 0 ]]; then echo "run as root" >&2; exit 1; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BASE="https://github.com/prometheus/node_exporter/releases/download/v${VER}"
NAME="node_exporter-${VER}.linux-${ARCH}"

curl -fsSL -o "$TMP/$NAME.tar.gz" "$BASE/$NAME.tar.gz"
curl -fsSL -o "$TMP/sha256sums.txt" "$BASE/sha256sums.txt"
(cd "$TMP" && grep " $NAME.tar.gz\$" sha256sums.txt | sha256sum -c -)
tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
install -m755 "$TMP/$NAME/node_exporter" /usr/local/bin/node_exporter

id node_exporter >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin node_exporter

cat > /etc/systemd/system/node_exporter.service <<EOF
[Unit]
Description=Prometheus Node Exporter
Wants=network-online.target
After=network-online.target

[Service]
User=node_exporter
Group=node_exporter
ExecStart=/usr/local/bin/node_exporter \
  --web.listen-address=${LISTEN} \
  --collector.systemd \
  --collector.filesystem.mount-points-exclude='^/(dev|proc|sys|run|var/lib/docker/.+|var/lib/kubelet/.+|snap/.+)(\$|/)'
Restart=always
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable node_exporter
systemctl restart node_exporter

if [[ -n "${MONITORING_HOST_IP:-}" && "$LISTEN" != 127.0.0.1:* ]] && command -v ufw >/dev/null; then
  ufw allow from "$MONITORING_HOST_IP" to any port 9100 proto tcp
fi

# Time sync matters: clock drift makes status and charts wrong.
timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes \
  || echo "WARNING: NTP is not synchronised; enable chrony or systemd-timesyncd" >&2

sleep 1
HOST="${LISTEN%:*}"; HOST="${HOST:-127.0.0.1}"
curl -fsS -o /dev/null "http://${HOST}:${LISTEN##*:}/metrics" \
  && echo "node_exporter ${VER} is running on ${LISTEN}"
