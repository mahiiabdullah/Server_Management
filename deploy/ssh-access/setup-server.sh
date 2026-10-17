#!/usr/bin/env bash
# One-time setup of a server for the SSH access workflow. Run as root ON THE
# SERVER, only after the change has been reviewed and approved:
#
#   sudo ./setup-server.sh <user_ca.pub> <deploy_ed25519.pub> <runner-ip>
#
#   user_ca.pub         public half of the CA created on bm2 (create-ca.sh)
#   deploy_ed25519.pub  public key the bm2 runner uses to reach this server
#   runner-ip           address the runner connects from (bm2's WireGuard IP, 10.0.0.12)
#
# What it changes (nothing else):
#   - /etc/ssh/user_ca.pub, /etc/ssh/principals/, /etc/ssh/revoked.krl (empty)
#   - groups bm-access and bm-sudo; /etc/sudoers.d/bm-access
#   - user bm-access-deploy, whose key may ONLY run bm-access-apply, ONLY from runner-ip
#   - /usr/local/sbin/bm-access-apply
#   - /etc/ssh/sshd_config.d/10-bm-access.conf, then `sshd -t` and a reload
# Existing logins (root keys, passwords) keep working: certificates are added
# alongside them. Disabling passwords is a separate, later decision.
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
[[ $# -eq 3 ]] || { sed -n '2,10p' "$0"; exit 2; }
CA_PUB=$1 DEPLOY_PUB=$2 RUNNER_IP=$3
HERE=$(cd "$(dirname "$0")" && pwd)

ssh-keygen -l -f "$CA_PUB" >/dev/null || { echo "not a public key: $CA_PUB" >&2; exit 1; }
ssh-keygen -l -f "$DEPLOY_PUB" >/dev/null || { echo "not a public key: $DEPLOY_PUB" >&2; exit 1; }
[[ "$RUNNER_IP" =~ ^[0-9.]+$|^[0-9a-fA-F:]+$ ]] || { echo "runner-ip must be an IP address" >&2; exit 1; }

install -m 0644 "$CA_PUB" /etc/ssh/user_ca.pub
install -d -m 0755 /etc/ssh/principals
[[ -s /etc/ssh/revoked.krl ]] || ssh-keygen -q -k -f /etc/ssh/revoked.krl /dev/null
install -m 0755 "$HERE/bm-access-apply" /usr/local/sbin/bm-access-apply

getent group bm-access >/dev/null || groupadd --system bm-access
getent group bm-sudo >/dev/null || groupadd --system bm-sudo

tmp=$(mktemp)
printf '%s\n' \
  '# Managed by the SSH access workflow (deploy/ssh-access/setup-server.sh).' \
  '%bm-sudo ALL=(ALL:ALL) NOPASSWD: ALL' \
  'bm-access-deploy ALL=(root) NOPASSWD: /usr/local/sbin/bm-access-apply' > "$tmp"
visudo -cf "$tmp" >/dev/null && install -m 0440 "$tmp" /etc/sudoers.d/bm-access
rm -f "$tmp"

id bm-access-deploy >/dev/null 2>&1 || useradd --system --create-home --shell /bin/bash bm-access-deploy
home=$(getent passwd bm-access-deploy | cut -d: -f6)
install -d -m 0700 -o bm-access-deploy -g bm-access-deploy "$home/.ssh"
printf 'restrict,from="%s",command="sudo -n /usr/local/sbin/bm-access-apply" %s\n' "$RUNNER_IP" "$(cat "$DEPLOY_PUB")" \
  > "$home/.ssh/authorized_keys"
chown bm-access-deploy:bm-access-deploy "$home/.ssh/authorized_keys"
chmod 0600 "$home/.ssh/authorized_keys"

printf '%s\n' \
  '# Managed by the SSH access workflow (deploy/ssh-access/setup-server.sh).' \
  'TrustedUserCAKeys /etc/ssh/user_ca.pub' \
  'AuthorizedPrincipalsFile /etc/ssh/principals/%u' \
  'RevokedKeys /etc/ssh/revoked.krl' \
  'LogLevel VERBOSE' > /etc/ssh/sshd_config.d/10-bm-access.conf

if ! grep -qiE '^\s*Include\s+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config; then
  echo "WARNING: sshd_config does not include sshd_config.d/*.conf; add those lines to sshd_config" >&2
fi
sshd -t
systemctl reload ssh 2>/dev/null || systemctl reload sshd
echo "bm-access setup complete. Keep this root session open and test a new login before closing it."
