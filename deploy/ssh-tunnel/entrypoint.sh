#!/bin/sh
# TUNNELS="19101=root@203.0.113.10 19102=ubuntu@203.0.113.11/9101"
#   local port = user@host[/remote exporter port on 127.0.0.1, default 9100]
set -eu
[ -n "${TUNNELS:-}" ] || { echo "TUNNELS is empty" >&2; exit 1; }
mkdir -p /root/.ssh /state
# Bind-mounted keys (Windows/macOS) have loose permissions; ssh needs 0600.
cp /run/secrets/tunnel_key /root/.ssh/id && chmod 600 /root/.ssh/id

run() {
  port="$1"; target="${2%/*}"; remote=9100
  case "$2" in */*) remote="${2##*/}" ;; esac
  while true; do
    echo "tunnel :$port -> $target 127.0.0.1:$remote starting"
    ssh -N -g -L "0.0.0.0:$port:127.0.0.1:$remote" -i /root/.ssh/id \
      -o BatchMode=yes -o ExitOnForwardFailure=yes \
      -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o ConnectTimeout=15 \
      -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/state/known_hosts \
      "$target" || true
    echo "tunnel :$port -> $target stopped; retrying in 5 s" >&2
    sleep 5
  done
}

for spec in $TUNNELS; do
  run "${spec%%=*}" "${spec#*=}" &
done
wait
