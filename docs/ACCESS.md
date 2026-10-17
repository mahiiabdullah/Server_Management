# SSH access workflow

Engineers request SSH access in the BM Monitor dashboard. The request becomes a
pull request that adds one entry to `access/grants.yaml`. CI checks it, an admin
approves and merges it, and a job on a trusted machine signs a short-lived SSH
certificate and creates the engineer's account. Nothing is emailed: the engineer
sees the status, the certificate and the connect command in the dashboard.

**Status (17 Oct 2026): code complete and tested in containers. Not deployed.**
No BM server has been changed for this feature. Steps 3–5 below change machines
and are done only when you decide to.

## How it fits together

| Piece | Where | What it does |
|---|---|---|
| Request form, "My SSH access", "All grants" | Dashboard (`/access`) | Builds the request with the same rules CI uses, opens the PR, shows status and certificate |
| `access/grants.yaml` | This repository | The one shared list of every grant |
| `access/policy.yaml` | This repository | Servers, maximum durations, admins, reserved names |
| `access-validate` workflow | GitHub-hosted runner, read-only, no secrets | Checks the PR: only the requester's new entry, valid key, known servers, limits; counts admin approvals |
| `access-apply` workflow | Self-hosted runner `bm-access` | Verifies approvals again, signs certificates, reconciles accounts, publishes status to the `access-state` branch |
| `accessctl` | `api/cmd/accessctl` | The tool behind both workflows |
| `bm-access-apply` | On each server, forced command | Creates, updates or disables only accounts in the `bm-access` group |

Code: rules in `api/internal/access`, GitHub client in `api/internal/ghclient`,
dashboard service in `api/internal/accessreq`, server scripts in `deploy/ssh-access/`.

## Security model

- **Approval is enforced twice.** CI reports missing approvals on the PR. Independently,
  `accessctl apply` refuses any grant unless a **merged** PR from the branch the
  dashboard created (`access/request-<id>`) added exactly that grant and had enough
  **admin** approvals **on its final commit**, not counting the requester. A direct
  push to `main` therefore cannot grant access (tested: the account is removed and
  the certificate revoked).
- **Free GitHub plan:** branch protection and required reviews are not available for
  private repositories on the free plan, so GitHub itself does not block merging.
  The apply-time check above is what enforces approval. With GitHub Pro or Team,
  also turn on branch protection for `main` (required review from CODEOWNERS,
  dismiss stale approvals, require the `access-validate` check).
- **Requests only add.** A request adds one new grant; it cannot change or remove
  other entries. Extending access is a new request. Revokes only remove one entry.
- **The tool and policy CI uses come from `main`,** never from the pull request.
- **Who can write to the repository:** only admins. Engineers never need GitHub
  access; the dashboard's bot opens their pull requests.
- **The bot's token cannot change workflows.** Give it *Contents: read and write* and
  *Pull requests: read and write* on this repository only, and **not** *Workflows*.
  GitHub then refuses any push from it that touches `.github/workflows/`, so it cannot
  make the self-hosted runner run new code.
- **The runner acts only on `main`:** the workflow says so, and `accessctl apply`
  refuses to run for any other ref.
- **The runner's server key can only run `bm-access-apply`,** only from the runner's
  address (`restrict,from=…,command=…` in `authorized_keys`). Tested: a shell command
  is ignored, and the same key from another machine is refused.
- **Existing accounts are never touched:** `bm-access-apply` only manages accounts in
  the `bm-access` group it creates. Personal accounts already on a server (for example
  `mahi` and `rakib` on bm3) are listed in `reserved_users`.
- **Revocation:** removing a grant disables the account (expired, principals removed,
  sessions ended; the home directory is kept) and adds the certificate's serial to
  the server's revocation list (`RevokedKeys`). Certificates also expire on their own.
- **Root and existing logins keep working;** nothing in setup disables them.

## Setup

### 1. GitHub (repository settings)

1. Make sure only admins have write access to the repository.
2. Create a **fine-grained personal access token** for the dashboard bot:
   *Repository access: only this repository · Contents: Read and write · Pull requests:
   Read and write · Metadata: Read · everything else: No access* (especially **not**
   Workflows). A GitHub App with the same permissions also works (see step 2).
3. Optional, with GitHub Pro/Team: branch protection on `main` as described above.

### 2. Dashboard (`.env`)

```bash
GITHUB_REPO=mahiiabdullah/Server_Management
GITHUB_TOKEN=github_pat_…            # from step 1
# or, as a GitHub App:
# GITHUB_APP_ID=…  GITHUB_APP_INSTALLATION_ID=…  GITHUB_APP_PRIVATE_KEY_FILE=/run/secrets/gh-app.pem
```

Restart the API. The "SSH access" page then lets signed-in users send requests.
Approvals are given on GitHub by the admins listed in `access/policy.yaml`.

### 3. The trusted machine (runner) — changes a machine; only when decided

Pick the machine that will hold the signing key and run `access-apply`. It must be
always on and able to reach the servers (WireGuard). Then, on it:

1. Install a [GitHub self-hosted runner](https://docs.github.com/en/actions/hosting-your-own-runners)
   for this repository with the label **`bm-access`**, running as its own service user.
2. `sudo deploy/ssh-access/create-ca.sh <runner-user>` creates `/etc/bm-access/ca`
   (the signing key; it never leaves this machine) and the deploy key.
3. Fill `/etc/bm-access/known_hosts` with each server's host key, verified out of band:
   `ssh-keyscan -t ed25519 10.0.0.13 >> /etc/bm-access/known_hosts`, then compare the
   fingerprint with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server.
4. Turn the apply job on: repository *Settings → Secrets and variables → Actions →
   Variables*, add `ACCESS_APPLY_ENABLED` = `true`. Until then `access-apply` is skipped,
   so nothing queues for a runner that does not exist.

### 4. Each server — changes the server; only when decided

Copy `deploy/ssh-access/` and the two public keys to the server, then as root:

```bash
sudo ./setup-server.sh /path/ca.pub /path/deploy_ed25519.pub <runner-ip>
```

Keep the root session open and test a new login before closing it. The script adds
certificate login alongside existing logins and touches nothing else (see its header).

### 5. Policy

List only servers that went through step 4 in `access/policy.yaml`. Production and
sudo requests need two admins, so add at least two more admins before allowing them.

## Testing

```bash
cd api && go test ./internal/access ./internal/httpapi -run 'Access|Apply|CheckPR|Sign'
```

The end-to-end test (`scripts/access-e2e/run.sh`, needs Docker) builds a throwaway
server container with the real `setup-server.sh` and `sshd`, a runner container with
the real `accessctl apply`, and a fake GitHub. It checks 17 behaviours: approved access
works, unapproved access is refused, the key alone (without a certificate) is refused,
the certificate does not work for root, existing accounts are untouched, the deploy
key cannot run commands or be used from elsewhere, revocation removes access and lists
the serial, and a grant tampered on `main` is not applied.
