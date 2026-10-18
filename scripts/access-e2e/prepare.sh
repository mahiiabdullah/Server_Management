#!/usr/bin/env bash
# Builds accessctl for Linux, creates throwaway test keys and fixtures, then runs the test.
# Usage: scripts/access-e2e/prepare.sh   (needs Go, Python 3, ssh-keygen and Docker)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
W=${E2E_WORKDIR:-$HERE/.work}
rm -rf "$W"; mkdir -p "$W/keys"
(cd "$HERE/../../api" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$W/accessctl" ./cmd/accessctl)
cp -r "$HERE/../../deploy/ssh-access" "$W/deploy"
cp "$HERE/fakegh.py" "$HERE/run.sh" "$W/"
for k in ca deploy rakib carol dave erin; do ssh-keygen -q -t ed25519 -N "" -C "$k" -f "$W/keys/$k"; done
python3 "$HERE/make_fixtures.py" "$W" 2>/dev/null || python "$HERE/make_fixtures.py" "$W"
docker pull -q ubuntu:24.04 >/dev/null; docker pull -q python:3.12-alpine >/dev/null
bash "$W/run.sh"
