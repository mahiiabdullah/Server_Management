"""Writes the policy, grants files and fake-GitHub fixtures for the e2e test."""
import datetime as dt, json, os, sys

E = sys.argv[1]
now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
req = (now - dt.timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
exp = (now + dt.timedelta(days=10)).strftime("%Y-%m-%dT%H:%M:%SZ")

def key(name):
    with open(os.path.join(E, "keys", name + ".pub")) as f:
        return f.read().strip()

def grant(gid, user, email, sudo=False):
    return (f"  - id: \"{gid}\"\n    user: {user}\n    email: {email}\n    servers:\n      - srv\n"
            f"    sudo: {str(sudo).lower()}\n    reason: End-to-end test of the access workflow\n"
            f"    public_key: {key(user)}\n    requested_at: {req}\n    expires: {exp}\n")


os.makedirs(os.path.join(E, "repo"), exist_ok=True)
os.makedirs(os.path.join(E, "data"), exist_ok=True)
with open(os.path.join(E, "repo", "policy.yaml"), "w", newline="\n") as f:
    f.write("servers:\n  srv: {host: 172.31.99.10, ssh_user: bm-access-deploy, production: false}\n"
            "max_days: 30\nmax_sudo_days: 7\nadmins:\n  - {github: admin1, email: admin1@example.com}\n")

g42 = grant("0042", "rakib", "rakib@example.com")
g43 = grant("0043", "carol", "carol@example.com")   # carol exists on the server, unmanaged
g44 = grant("0044", "dave", "dave@example.com")     # never approved
files = {
    "grants1.yaml": "grants:\n" + g42 + g43 + g44,     # first run
    "grants2.yaml": "grants:\n" + g43 + g44,           # after revoking 0042
}
for n, c in files.items():
    with open(os.path.join(E, "repo", n), "w", newline="\n") as f:
        f.write(c)

def pr(num, branch):
    return [{"number": num, "state": "closed", "merged_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"), "html_url": f"https://example/pull/{num}",
             "head": {"sha": f"h{num}", "ref": branch}, "base": {"ref": "main"}, "labels": [{"name": "access-request"}]}]

d = os.path.join(E, "data")
for num, gid in ((42, "0042"), (43, "0043")):
    branch = f"access/request-{gid}"
    json.dump(pr(num, branch), open(os.path.join(d, f"prs_{branch.replace('/', '_')}.json"), "w"))
    json.dump([{"user": {"login": "admin1"}, "state": "APPROVED", "commit_id": f"h{num}"}], open(os.path.join(d, f"reviews_{num}.json"), "w"))
    with open(os.path.join(d, f"grants_h{num}.yaml"), "w", newline="\n") as f:
        f.write(files["grants1.yaml"])
print("fixtures written")
