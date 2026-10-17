"""Fake GitHub API for the access e2e test. Serves PRs, reviews and file contents
from JSON fixtures in /data. Read-only; logs every request."""
import base64, json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

DATA = "/data"

def load(name, default):
    p = os.path.join(DATA, name)
    if not os.path.exists(p):
        return default
    with open(p, "rb") as f:
        return f.read() if name.endswith(".yaml") else json.load(f)

class H(BaseHTTPRequestHandler):
    def send(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        u = urlparse(self.path)
        q = parse_qs(u.query)
        parts = u.path.strip("/").split("/")  # repos o r ...
        print("GET", self.path, file=sys.stderr, flush=True)
        if parts[3:] == ["pulls"]:
            branch = q["head"][0].split(":", 1)[1].replace("/", "_")
            return self.send(200, load(f"prs_{branch}.json", []))
        if len(parts) == 6 and parts[3] == "pulls" and parts[5] == "reviews":
            return self.send(200, load(f"reviews_{parts[4]}.json", []))
        if parts[3] == "contents":
            ref = q["ref"][0]
            content = load(f"grants_{ref}.yaml", None)
            if content is None:
                return self.send(404, {"message": "Not Found"})
            return self.send(200, {"content": base64.b64encode(content).decode(), "encoding": "base64", "sha": "blob"})
        self.send(404, {"message": "Not Found"})

    def log_message(self, *a):
        pass

HTTPServer(("0.0.0.0", 8000), H).serve_forever()
