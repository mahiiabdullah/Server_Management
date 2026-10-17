#!/usr/bin/env bash
# Creates the SSH certificate authority and the runner's deploy key ON bm2.
# Run as root once, after approval:  sudo ./create-ca.sh <runner-user>
#
# Creates (readable only by root and the runner's service account):
#   /etc/bm-access/ca                 CA private key  (never copy it anywhere)
#   /etc/bm-access/ca.pub             CA public key   (goes to every server)
#   /etc/bm-access/deploy_ed25519     key the runner uses to reach servers
#   /etc/bm-access/deploy_ed25519.pub (goes to every server)
#   /etc/bm-access/known_hosts        pinned host keys of the servers (fill in, see docs/ACCESS.md)
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
RUNNER_USER=${1:?usage: create-ca.sh <runner-user>}
id "$RUNNER_USER" >/dev/null
[[ -e /etc/bm-access/ca ]] && { echo "/etc/bm-access/ca already exists; not overwriting" >&2; exit 1; }
install -d -m 0750 -o root -g "$RUNNER_USER" /etc/bm-access
ssh-keygen -q -t ed25519 -N "" -C "bm-access user CA $(date +%F)" -f /etc/bm-access/ca
ssh-keygen -q -t ed25519 -N "" -C "bm-access deploy (bm2 runner)" -f /etc/bm-access/deploy_ed25519
touch /etc/bm-access/known_hosts
chown root:"$RUNNER_USER" /etc/bm-access/ca /etc/bm-access/deploy_ed25519 /etc/bm-access/known_hosts
chmod 0640 /etc/bm-access/ca /etc/bm-access/deploy_ed25519
chmod 0644 /etc/bm-access/ca.pub /etc/bm-access/deploy_ed25519.pub /etc/bm-access/known_hosts
echo "CA public key (install on every server with setup-server.sh):"
cat /etc/bm-access/ca.pub
