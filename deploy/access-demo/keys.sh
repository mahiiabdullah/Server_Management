#!/bin/sh
# Creates the demo's signing (CA) key and deploy key once. The private keys stay
# in the runner's volume; only the public halves go to the demo server.
set -eu
apk add -q --no-cache openssh-keygen >/dev/null
[ -s /ca/ca ] || ssh-keygen -q -t ed25519 -N "" -C "bm-access demo CA" -f /ca/ca
[ -s /ca/deploy_ed25519 ] || ssh-keygen -q -t ed25519 -N "" -C "bm-access demo deploy key" -f /ca/deploy_ed25519
cp /ca/ca.pub /ca/deploy_ed25519.pub /pub/
# The runner image's user is uid 1001.
chown -R 1001:1001 /ca /runner-state
chmod 700 /ca
chmod 600 /ca/ca /ca/deploy_ed25519
echo "demo keys ready"
