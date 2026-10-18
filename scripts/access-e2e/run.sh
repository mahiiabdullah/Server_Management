#!/usr/bin/env bash
# End-to-end test of the SSH access workflow, entirely in throwaway containers:
#   bmt-srv     a "server": real sshd, configured by the real setup-server.sh
#   bmt-runner  the "automation machine": runs the real accessctl apply
#   bmt-gh      fake GitHub API (merged PRs + admin approvals)
#   bmt-evil    another machine holding a stolen deploy key
set -u
E=$(cd "$(dirname "$0")" && pwd -W 2>/dev/null || pwd)
export MSYS_NO_PATHCONV=1
NET=bmtest
pass=0; failn=0
ok()  { echo "  PASS  $*"; pass=$((pass+1)); }
bad() { echo "  FAIL  $*"; failn=$((failn+1)); }
check() { local name=$1; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }

cleanup() { docker rm -f bmt-srv bmt-runner bmt-gh bmt-evil >/dev/null 2>&1; docker network rm $NET >/dev/null 2>&1; }
cleanup
docker network create --subnet 172.31.99.0/24 $NET >/dev/null

docker run -d --name bmt-gh --network $NET --ip 172.31.99.30 -v "$E/fakegh.py:/fakegh.py:ro" -v "$E/data:/data:ro" python:3.12-alpine python /fakegh.py >/dev/null
for c in srv:10 runner:20 evil:40; do
  docker run -d --name "bmt-${c%%:*}" --network $NET --ip "172.31.99.${c##*:}" -v "$E:/e2e:ro" ubuntu:24.04 sleep infinity >/dev/null
done

echo "== prepare containers"
docker exec bmt-srv bash -c '
  set -e; apt-get update -qq >/dev/null; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-server sudo procps >/dev/null
  mkdir -p /run/sshd
  printf "#!/bin/sh\npkill -HUP -x sshd; exit 0\n" > /usr/local/bin/systemctl; chmod +x /usr/local/bin/systemctl
  useradd -m carol                                     # an existing account NOT managed by bm-access
  cp -r /e2e/deploy /root/deploy
  /root/deploy/setup-server.sh /e2e/keys/ca.pub /e2e/keys/deploy.pub 172.31.99.20
  /usr/sbin/sshd' && ok "setup-server.sh ran, sshd -t passed" || bad "setup-server.sh"
for c in runner evil; do
  docker exec bmt-$c bash -c '
    set -e; apt-get update -qq >/dev/null; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-client >/dev/null
    mkdir -p /k; cp /e2e/keys/* /k/; chmod 600 /k/*; ssh-keyscan -t ed25519 172.31.99.10 > /k/known_hosts 2>/dev/null'
done

APPLY='-e GITHUB_API_URL=http://172.31.99.30:8000 -e GITHUB_REPOSITORY=o/r -e GITHUB_TOKEN=test'
apply() {
  docker exec $APPLY bmt-runner /e2e/accessctl apply --grants "/e2e/repo/$1" --policy "/e2e/repo/${2:-policy.yaml}" \
    --state-dir /state --ca /k/ca --deploy-key /k/deploy --known-hosts /k/known_hosts --commit "$1"
}
SSH='ssh -o BatchMode=yes -o IdentitiesOnly=yes -o UserKnownHostsFile=/k/known_hosts -o ConnectTimeout=5'

echo "== run 1: grants 0042 rakib (approved), 0043 carol (approved, but an unmanaged account), 0044 dave (no approval)"
apply grants1.yaml; echo "  (apply exit $?; expected 1 because carol is refused)"
status=$(docker exec bmt-runner cat /state/status.json)
check "rakib's grant is active"                    grep -q '"0042"' <<<"$(docker exec bmt-runner sh -c 'grep -A2 "\"0042\"" /state/status.json')"
check "dave's grant is awaiting approval"          sh -c "docker exec bmt-runner sh -c 'grep -A2 \"\\\"0044\\\"\" /state/status.json' | grep -q awaiting_approval"
check "certificate issued for rakib"               docker exec bmt-runner test -s /state/certs/0042-cert.pub
check "no certificate for dave"                    sh -c '! docker exec bmt-runner test -e /state/certs/0044-cert.pub'
out=$(docker exec bmt-runner $SSH -i /k/rakib -o CertificateFile=/state/certs/0042-cert.pub rakib@172.31.99.10 'id -un; sudo -n true 2>/dev/null && echo SUDO || echo NOSUDO' 2>&1)
check "rakib logs in with key + certificate"      grep -qx rakib <<<"$out"
check "rakib has no sudo"                          grep -qx NOSUDO <<<"$out"
out=$(docker exec bmt-runner $SSH -i /k/rakib rakib@172.31.99.10 id 2>&1)
check "rakib's key alone (no certificate) is refused" grep -q "Permission denied" <<<"$out"
out=$(docker exec bmt-runner $SSH -i /k/rakib -o CertificateFile=/state/certs/0042-cert.pub root@172.31.99.10 id 2>&1)
check "rakib's certificate does not work for root" grep -q "Permission denied" <<<"$out"
check "dave's account was not created"             sh -c '! docker exec bmt-srv id dave >/dev/null 2>&1'
check "carol (unmanaged) was not touched"          sh -c "docker exec bmt-srv id -nG carol | grep -qvw bm-access && ! docker exec bmt-srv test -e /etc/ssh/principals/carol"
out=$(docker exec bmt-runner $SSH -i /k/deploy bm-access-deploy@172.31.99.10 'id; cat /etc/shadow' 2>&1)
check "deploy key cannot run commands (forced to bm-access-apply)" sh -c "grep -q 'ERROR bad header' <<<\"$out\" && ! grep -q uid= <<<\"$out\""
out=$(docker exec bmt-evil $SSH -i /k/deploy bm-access-deploy@172.31.99.10 id 2>&1)
check "deploy key from another machine is refused" grep -q "Permission denied" <<<"$out"

echo "== run 2: rakib's grant revoked (removed from the file)"
docker exec bmt-runner cp /state/certs/0042-cert.pub /tmp/old-cert.pub
apply grants2.yaml; echo "  (apply exit $?)"
out=$(docker exec bmt-runner $SSH -i /k/rakib -o CertificateFile=/tmp/old-cert.pub rakib@172.31.99.10 id 2>&1)
check "rakib's old certificate no longer works"    grep -q "Permission denied" <<<"$out"
docker cp bmt-runner:/tmp/old-cert.pub "$E/old-cert.pub" >/dev/null && docker cp "$E/old-cert.pub" bmt-srv:/tmp/old-cert.pub >/dev/null
check "serial 42 is in the server's revocation list" sh -c 'docker exec bmt-srv ssh-keygen -Q -f /etc/ssh/revoked.krl /tmp/old-cert.pub | grep -q REVOKED'
check "rakib's account is disabled, home kept"     sh -c "docker exec bmt-srv chage -l rakib | grep -q 'Account expires.*1970' && docker exec bmt-srv test -d /home/rakib"

echo "== run 3: tamper - someone edits main to give carol sudo without a pull request"
docker exec bmt-runner sh -c "sed 's/sudo: false/sudo: true/' /e2e/repo/grants2.yaml > /tmp/tampered.yaml"
docker exec $APPLY bmt-runner /e2e/accessctl apply --grants /tmp/tampered.yaml --policy /e2e/repo/policy.yaml --state-dir /state --ca /k/ca --deploy-key /k/deploy --known-hosts /k/known_hosts --commit tampered >/dev/null 2>&1
check "tampered grant is not applied (awaiting approval)" sh -c "docker exec bmt-runner sh -c 'grep -A2 \"\\\"0043\\\"\" /state/status.json' | grep -q awaiting_approval"

echo "== run 4: erin's grant is for the access group 'workers', which contains srv"
apply grants4.yaml policy-group.yaml >/dev/null 2>&1
out=$(docker exec bmt-runner $SSH -i /k/erin -o CertificateFile=/state/certs/0045-cert.pub erin@172.31.99.10 id -un 2>&1)
check "erin logs in through her group grant"       grep -qx erin <<<"$out"

echo "== run 5: srv is removed from the group (a reviewed policy change)"
apply grants4.yaml policy-group-empty.yaml >/dev/null 2>&1
out=$(docker exec bmt-runner $SSH -i /k/erin -o CertificateFile=/state/certs/0045-cert.pub erin@172.31.99.10 id -un 2>&1)
check "erin loses access when srv leaves the group" grep -q "Permission denied" <<<"$out"
check "erin's account is disabled, home kept"       sh -c "docker exec bmt-srv chage -l erin | grep -q 'Account expires.*1970' && docker exec bmt-srv test -d /home/erin"

echo
echo "RESULT: $pass passed, $failn failed"
docker logs bmt-gh 2>&1 | sort | uniq -c | sed 's/^/  github /' | head -8
cleanup
[[ $failn -eq 0 ]]
