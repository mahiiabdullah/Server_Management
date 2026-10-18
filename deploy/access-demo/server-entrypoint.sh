#!/bin/bash
# Demo server: run the real one-time setup with the demo CA and deploy key,
# publish this server's host key for the runner to pin, then run sshd.
set -euo pipefail

for _ in $(seq 60); do
  [[ -s /pub/ca.pub && -s /pub/deploy_ed25519.pub ]] && break
  sleep 1
done
[[ -s /pub/ca.pub ]] || { echo "demo keys missing in /pub" >&2; exit 1; }

if [[ ! -e /etc/ssh/.bm-access-demo-setup ]]; then
  /opt/ssh-access/setup-server.sh /pub/ca.pub /pub/deploy_ed25519.pub "${RUNNER_IP:?}"
  touch /etc/ssh/.bm-access-demo-setup
fi
cp /etc/ssh/ssh_host_ed25519_key.pub /pub/host_ed25519.pub
echo "demo server ready: certificate login on, deploy key allowed only from ${RUNNER_IP}"
exec /usr/sbin/sshd -D -e
