# SSH access workflow

Engineers request SSH access in the BM Monitor dashboard, to chosen servers or to
a whole **access group** (for example every server of `puku-bot`). The request
becomes a pull request that adds one entry to `access/grants.yaml`. CI checks it, an admin
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
| `access/policy.yaml` | This repository | Servers, access groups, maximum durations, admins, reserved names |
| "SSH access" card on each group page | Dashboard (`/groups/…`) | Compares the dashboard group with its access group and opens the policy pull request that syncs them |
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
- **Group grants follow the group.** A grant for an access group covers the
  group's servers at every apply: a server added to the group is covered, a server
  removed loses the account. Changing a group is a change to `access/policy.yaml`,
  so it is a reviewed pull request (the dashboard opens it, labelled
  `access-policy`). Sudo cannot be requested for a group.
- **Production needs two approvals, per server.** The apply job reaches a
  production server only for grants approved by two admins. If a group later gains
  a production server, grants approved by one admin are not applied there (their
  other servers keep working) and the dashboard says why.
- **Policy changes can be enforced too (optional).** With the repository variable
  `ACCESS_VERIFY_POLICY=true`, the apply job only uses a policy that came from a
  merged pull request approved by an admin other than its author (for a dashboard
  proposal: other than the admin who requested it). Otherwise it keeps applying the
  last approved policy, saved on the `access-state` branch, so revocations keep
  working. CI shows the same check on policy pull requests. This needs at least two
  admins in the policy, because nobody can approve their own pull request.
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
2. Create the dashboard bot as a **GitHub App**:
   `python scripts/create-github-app.py mahiiabdullah/Server_Management`, then click
   *Create GitHub App* and install it on **this repository only**. The App gets
   *Contents* and *Pull requests* (read and write) and *Metadata* (read), never
   *Workflows*. Its key is saved to `secrets/gh-app.pem` (gitignored, mounted into the
   API container) and the script prints the `.env` lines.

   Why an App and not a personal token: pull requests opened with a personal token are
   authored by the token's owner, and GitHub does not let an author approve their own
   pull request. If that owner is an admin, their approval can never count. A token
   works only if it belongs to a separate bot account with the same permissions.
3. Optional, with GitHub Pro/Team: branch protection on `main` as described above.

### 2. Dashboard (`.env`)

```bash
GITHUB_REPO=mahiiabdullah/Server_Management
GITHUB_APP_ID=…                      # printed by the script in step 1
GITHUB_APP_INSTALLATION_ID=…
GITHUB_APP_PRIVATE_KEY_FILE=/run/bm-secrets/gh-app.pem
# or a bot account's token instead: GITHUB_TOKEN=github_pat_…
```

Restart the API. The "SSH access" page then lets signed-in users send requests.
Approvals are given on GitHub by the admins listed in `access/policy.yaml`. A requester
never counts as their own approver, so an admin's own request needs another admin.

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
5. Once there are two admins in `access/policy.yaml`: merge one policy pull request that
   another admin approved (adding the second admin is a good one), then add the variable
   `ACCESS_VERIFY_POLICY` = `true`. From then on an unreviewed change to the policy is
   ignored by the apply job.

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

### 6. Access groups

On a group page, the **SSH access** card shows the group's servers next to its access
group. An admin presses **Make this an access group** (or **Propose access change**);
the bot opens a pull request that sets `groups.<name>` in `access/policy.yaml` to the
group's servers that the policy knows. Servers not in the policy's server list (SSH setup
not done) are left out and listed. Another admin approves and merges it. Then anyone can
choose **By group** in the request form.

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
the serial, a grant tampered on `main` is not applied, a group grant gives access, and
removing the server from the group removes it (20 checks).
