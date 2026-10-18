#!/bin/bash
# Demo runner: pin the demo server's host key, register with GitHub once (the
# registration is kept in a volume), then wait for access-apply jobs.
set -euo pipefail
cd /home/runner

for _ in $(seq 120); do
  [[ -s /pub/host_ed25519.pub ]] && break
  sleep 1
done
[[ -s /pub/host_ed25519.pub ]] || { echo "demo server host key not published" >&2; exit 1; }
echo "${SERVER_IP:?} $(cut -d' ' -f1,2 /pub/host_ed25519.pub)" > /etc/bm-access/known_hosts

# Registration files live in the volume, so a restart does not need a new token.
for f in .runner .credentials .credentials_rsaparams; do
  ln -sf "/runner-state/$f" "$f"
done
if [[ ! -s /runner-state/.runner ]]; then
  : "${RUNNER_TOKEN:?Set ACCESS_DEMO_RUNNER_TOKEN in .env: GitHub repo → Settings → Actions → Runners → New self-hosted runner (the token expires after 1 hour)}"
  ./config.sh --unattended --replace --url "${REPO_URL:?}" --token "$RUNNER_TOKEN" \
    --name bm-access-demo --labels bm-access --work _work
fi
exec ./run.sh
