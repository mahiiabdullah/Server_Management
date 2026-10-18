# SSH access demo (one machine, no real server)

Shows the whole workflow end to end: a key pair, a request in the dashboard, a pull
request, an admin approval, the apply job signing a certificate, an SSH login, and a
revoke that cuts the login off. Everything runs in Docker on this machine:

| Container | Role |
|---|---|
| `demo-server` | A throwaway server with real OpenSSH, prepared by the real `deploy/ssh-access/setup-server.sh`. Listens on `localhost:2222`. |
| `runner` | A GitHub self-hosted runner (label `bm-access`) that runs the repository's `access-apply` workflow. It holds the demo CA key in a Docker volume. |

No BM server is involved: in `access/policy.yaml`, bm3 is `ready: false`, so the runner
never connects to it, and only `demo` can be requested.

## One-time setup (about 5 minutes)

1. **Runner token.** GitHub repo → *Settings → Actions → Runners → New self-hosted runner*.
   Copy the token from the `./config.sh … --token XXXX` line and put it in `.env`:
   `ACCESS_DEMO_RUNNER_TOKEN=XXXX` (it expires after an hour; it is only needed once).
2. **Turn the apply job on.** *Settings → Secrets and variables → Actions → Variables* →
   `ACCESS_APPLY_ENABLED` = `true`.
3. **Start the demo:**
   ```bash
   docker compose -f docker-compose.access-demo.yml up -d --build
   ```
   The runner shows as **Idle** under *Settings → Actions → Runners* within a minute.

## The demo

1. **Make a key pair** on your PC (PowerShell or Git Bash):
   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/demo_key -C "demo"
   ```
   `demo_key` is the **private key** (stays on the PC, never pasted anywhere);
   `demo_key.pub` is the **public key**.
2. **Request access** as a non-admin user (an admin cannot approve their own request):
   *SSH access* → By server → tick **demo** → days, reason → paste the contents of
   `demo_key.pub` → *Send request*.
3. **Approve.** Open the pull request the bot created → *Files changed* → *Review changes*
   → *Approve* → *Merge*.
4. **Wait about a minute.** The merge starts `access-apply` on the runner (watch it under
   *Actions*). It re-checks the approval, signs a certificate and creates the account on
   the demo server. The request card turns **Active** and shows the certificate.
5. **Log in.** Save the certificate as `~/.ssh/demo_key-cert.pub` (next to the key, so
   SSH picks it up), then:
   ```bash
   ssh -i ~/.ssh/demo_key -p 2222 <unix user shown on the card>@localhost
   ```
   You are in. Without the certificate (move the file away), the same key is refused.
6. **Revoke.** As admin: *SSH access → All grants → Revoke* → approve and merge the revoke
   pull request. After the next apply, the same `ssh` command is refused, and the
   certificate's serial is on the server's revocation list.

## Stop and clean up

```bash
docker compose -f docker-compose.access-demo.yml down        # keeps keys and runner registration
docker compose -f docker-compose.access-demo.yml down -v     # deletes them too
```
Then remove the runner under *Settings → Actions → Runners*, set `ACCESS_APPLY_ENABLED`
back to `false` (or delete it), and remove `demo` from `access/policy.yaml`.

## Notes

- The demo CA is generated on first start and never leaves the `ca-keys` volume. A real
  setup uses `deploy/ssh-access/create-ca.sh` on the real runner machine (docs/ACCESS.md).
- The runner pins the demo server's host key from the server itself (shared volume); a
  real setup pins host keys verified out of band.
- The hourly apply also runs while the demo is up; stop it with `down` when finished.
