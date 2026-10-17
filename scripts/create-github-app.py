#!/usr/bin/env python3
"""Create the GitHub App the dashboard uses to open SSH access pull requests.

Uses GitHub's app-manifest flow, so the only manual steps are two clicks in the
browser ("Create GitHub App", then "Install" on this repository only).

    python scripts/create-github-app.py mahiiabdullah/Server_Management

Writes the App's private key to secrets/gh-app.pem and prints the .env lines.
The App gets Contents + Pull requests (read/write) and Metadata (read) only;
no Workflows, no webhooks. Pull requests it opens are authored by the App's bot
user, so a human admin can approve them.
"""
import html
import json
import os
import secrets
import sys
import urllib.request
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse

PORT = 8765
BASE = f"http://localhost:{PORT}"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KEY_PATH = os.path.join(ROOT, "secrets", "gh-app.pem")

if len(sys.argv) != 2 or "/" not in sys.argv[1]:
    sys.exit("usage: create-github-app.py <owner>/<repo>")
REPO = sys.argv[1]
STATE = secrets.token_urlsafe(24)
app = {}

MANIFEST = {
    "name": f"bm-access-bot-{secrets.token_hex(3)}",
    "url": f"https://github.com/{REPO}",
    "description": "Opens SSH access request pull requests for BM Monitor.",
    "hook_attributes": {"url": "https://example.invalid/", "active": False},
    "redirect_url": f"{BASE}/created",
    "setup_url": f"{BASE}/installed",
    "public": False,
    "default_permissions": {"contents": "write", "pull_requests": "write", "metadata": "read"},
    "default_events": [],
}


def page(body):
    return f"<!doctype html><meta charset=utf-8><title>GitHub App setup</title><body style='font-family:sans-serif;max-width:40em;margin:3em auto'>{body}</body>".encode()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send(self, code, body, location=None):
        self.send_response(code)
        if location:
            self.send_header("Location", location)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        url = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        if url.path == "/":
            self.send(200, page(
                "<h2>Create the BM access bot</h2>"
                f"<p>GitHub will ask you to confirm creating <b>{html.escape(MANIFEST['name'])}</b> "
                "with Contents + Pull requests access. Click <b>Create GitHub App</b> there.</p>"
                f"<form method=post action='https://github.com/settings/apps/new?state={STATE}'>"
                f"<input type=hidden name=manifest value='{html.escape(json.dumps(MANIFEST), quote=True)}'>"
                "<button style='font-size:1.2em'>Continue to GitHub</button></form>"))
        elif url.path == "/created":
            if q.get("state") != STATE or "code" not in q:
                return self.send(400, page("State mismatch. Start again."))
            req = urllib.request.Request(
                f"https://api.github.com/app-manifests/{q['code']}/conversions", method="POST",
                headers={"Accept": "application/vnd.github+json", "User-Agent": "bm-monitor-setup"})
            with urllib.request.urlopen(req, timeout=30) as r:
                conv = json.load(r)
            os.makedirs(os.path.dirname(KEY_PATH), exist_ok=True)
            with open(KEY_PATH, "w", newline="\n") as f:
                f.write(conv["pem"])
            app.update(id=conv["id"], slug=conv["slug"])
            print(f"App created: {conv['slug']} (id {conv['id']}); key saved to {KEY_PATH}", flush=True)
            # Install it next; GitHub returns to /installed with the installation id.
            self.send(302, b"", f"https://github.com/apps/{conv['slug']}/installations/new")
        elif url.path == "/installed":
            if not app or "installation_id" not in q:
                return self.send(400, page("Missing installation id."))
            app["installation_id"] = int(q["installation_id"])
            self.send(200, page(
                "<h2>Done</h2><p>The bot is installed. You can close this tab and go back to the terminal.</p>"))
            print("\nAdd to .env:\n"
                  f"GITHUB_REPO={REPO}\n"
                  f"GITHUB_APP_ID={app['id']}\n"
                  f"GITHUB_APP_INSTALLATION_ID={app['installation_id']}\n"
                  "GITHUB_APP_PRIVATE_KEY_FILE=/run/bm-secrets/gh-app.pem", flush=True)
            raise KeyboardInterrupt
        else:
            self.send(404, page("Not found"))


print(f"Open {BASE}/ in your browser (signed in to GitHub as the repository owner).", flush=True)
try:
    webbrowser.open(f"{BASE}/")
except Exception:
    pass
server = HTTPServer(("127.0.0.1", PORT), Handler)
try:
    server.serve_forever()
except KeyboardInterrupt:
    pass
