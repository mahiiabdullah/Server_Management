# BM Monitor

Monitoring and cost management for a fleet of bare-metal servers.

Node Exporter runs on every server, and Prometheus scrapes them. The Go API keeps the
inventory, pricing, alerts and users in PostgreSQL, and turns Prometheus data into
dashboard-ready JSON. A Next.js web app shows the fleet, each server, costs, alerts and
insights. Every number on screen comes from the inventory or live telemetry; nothing is
hardcoded, and costs are never guessed.

| Document | Contents |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Inspection report, architecture, decisions, phase status, test results |
| [docs/API.md](docs/API.md) | REST API contract |
| [docs/RUNBOOK.md](docs/RUNBOOK.md) | Production deployment, adding servers, backups and restore, troubleshooting |
| [docs/ACCESS.md](docs/ACCESS.md) | SSH access requests: pull-request approval, certificates, setup and security model |
| [docs/ACCESS-DEMO.md](docs/ACCESS-DEMO.md) | Demo of the whole SSH access flow on one machine (no real server) |

## Run it locally (Windows/macOS/Linux with Docker Desktop)

```bash
cp .env.example .env     # fill in the Required section (see the comments in the file)
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d --build
```

Open **http://localhost:8088** and sign in with `ADMIN_EMAIL` / `ADMIN_PASSWORD`.

The local override serves plain HTTP on port 8088. Your PC is not on the servers' WireGuard
network, so a small `tunnel` service keeps SSH forwards open to each server's Node Exporter, which
listens on `127.0.0.1` only there. Nothing is exposed on the servers. Set `TUNNELS` and
`TUNNEL_KEY_FILE` in `.env` (see `docker-compose.local.yml`), then register each server in the UI as
`http://172.30.20.10:<tunnel port>/metrics`. Details and undo steps: [docs/RUNBOOK.md](docs/RUNBOOK.md), section 5.

Stop with `docker compose -f docker-compose.yml -f docker-compose.local.yml down` (add `-v` to
also delete the database and metric history).

## Layout

```
api/                       Go backend: cmd/api, internal/{config,db,auth,inventory,targets,metrics,cost,alerts,insights,audit,httpapi}, migrations/
web/                       Next.js 16 frontend: app/, components/, lib/, tests/ (Vitest), tests-e2e/ (Playwright)
prometheus/prometheus.yml  scrape config (file-based discovery from the targets volume)
nginx/                     nginx.conf (production TLS), nginx.local.conf (local HTTP)
deploy/node-exporter/      install.sh: run once on every monitored server
docker-compose.yml         production stack; docker-compose.local.yml adds the local overrides
```

## Tests

```bash
cd api && go vet ./... && go test ./...      # unit + integration (real PostgreSQL, see below)
cd web && npm run lint && npx tsc --noEmit && npm test
cd web && E2E_EMAIL=... E2E_PASSWORD=... npx playwright test   # against a running stack
```

Go integration tests use `TEST_DATABASE_URL` when it is set. Otherwise they download and run an
embedded PostgreSQL 17 (about 30 s the first time), so Docker is not needed for them. The
Playwright tests need a running stack with at least one online server.
