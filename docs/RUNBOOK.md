# Runbook

## 1. Deploy to the monitoring host (bm2, 51.38.55.124)

Not done yet: the server is read-only until the owner approves changes. bm2 already has
Docker 29 and Compose v5, and ports 80, 443 and 9100 are free (inspection, ARCHITECTURE.md section 5).

1. Copy the repository to the host (for example `~/bm-monitor`). Then `cp .env.example .env` and fill in:
   - Generated secrets: `openssl rand -hex 24` for `POSTGRES_PASSWORD`, `openssl rand -base64 32` for the two secrets.
   - `PUBLIC_URL`: the https address users will open.
   - `ALLOWED_TARGET_CIDRS`: `10.0.0.0/24` if every server is on the WireGuard network. Otherwise list each public IP as `/32`.
   - `BILLING_TIMEZONE`, `BILLING_CURRENCY`, and the `SMTP_*` settings (Gmail app password).
2. TLS: put `fullchain.pem` and `privkey.pem` in `nginx/certs/` (Let's Encrypt via certbot), and set `server_name` in `nginx/nginx.conf`.
3. `docker compose up -d --build`, then `docker compose ps`: every service should be `healthy`.
4. Firewall (ufw on bm2): allow 80/443 from the internet. Allow 9100 only from the Docker bridge
   (`172.30.1.0/24`) and from the monitoring host's WireGuard address. **Show these rules to the owner before applying them.**

## 2. Add a server

1. On the server, as root: `MONITORING_HOST_IP=<monitoring host IP> ./deploy/node-exporter/install.sh`.
   It verifies the download checksum, installs a hardened systemd unit, and opens 9100 to the monitoring host only.
2. In the UI: *Servers → Add server*. Enter the exporter URL, press **Check**, then save.
   Prometheus starts scraping within about 30 s, with no restart or rebuild.
3. On the server's page (admin), assign a pricing plan from the right date.

To remove a server: *Disable* stops monitoring and billing time and can be undone.
*Delete* (admin, type the name to confirm) removes it from the inventory but keeps its history and cost records.

## 3. Backups and restore

PostgreSQL holds everything that matters: inventory, pricing, users, rules, incidents and audit.
Prometheus history rebuilds itself by scraping; keep the `promdata` volume if history matters.

```bash
# nightly (cron on the monitoring host); keep 14 days
docker compose exec -T postgres pg_dump -U bm -Fc bm > /backups/bm-$(date +%F).dump
find /backups -name 'bm-*.dump' -mtime +14 -delete

# restore
docker compose stop api
docker compose exec -T postgres pg_restore -U bm -d bm --clean --if-exists < /backups/bm-YYYY-MM-DD.dump
docker compose start api
```

Test a restore every month on a scratch copy: `docker compose -p bm-restore-test ...`.

## 4. Troubleshooting

| Symptom | Check |
|---|---|
| A server stays **unknown** | `docker compose exec prometheus wget -qO- localhost:9090/api/v1/targets` shows the scrape error. Is 9100 open to the monitoring host? Is the address inside `ALLOWED_TARGET_CIDRS`? |
| A server shows **offline** | Node Exporter is down or the firewall changed: `systemctl status node_exporter` on the server. |
| "Monitoring unavailable" banner and alert | Prometheus is down: `docker compose ps prometheus` and `docker compose logs prometheus`. |
| No alert emails | Settings → Notifications: is email "configured"? Press **Send test**. A failed send shows its error there, and failed notifications retry 5 times with backoff. |
| Locked out | An admin can unlock a user in Settings → Users. If no admin can sign in, reset a password with `docker compose exec postgres psql -U bm -d bm` (the hash must be argon2id; easiest is to create a new admin with the API's first-admin bootstrap on an empty users table). |
| Request failed with an id | Search the API log for it: `docker compose logs api \| grep <request id>`. |

## 5. Scraping through SSH tunnels (local dashboard)

Used while the dashboard runs on a workstation outside the WireGuard network. Current fleet:

| Server | Address | Exporter on the server | Tunnel port |
|---|---|---|---|
| bm1, control plane | root@103.174.50.75 (wg 10.0.0.11) | BM Monitor node_exporter 1.9.1 on `127.0.0.1:9100` | 19101 |
| bm2, worker | root@51.38.55.124 (wg 10.0.0.12) | BM Monitor node_exporter 1.9.1 on `127.0.0.1:9100` | 19102 |
| bm3, poridhi-test | mahi@103.174.50.21 (wg 10.0.0.13) | BM Monitor node_exporter 1.9.1 on `127.0.0.1:9101` | 19103 |

bm3 also runs the **snap** `node-exporter` on `*:9100`. It was left untouched, but it is not used:
snap confinement gives it a private mount namespace, so it reports wrong filesystem sizes
(`/` = 34 GB instead of the real 943 GB).

**What was changed on each server** (2026-10-15):

1. Installed `/usr/local/bin/node_exporter`, the system user `node_exporter`, and the unit
   `/etc/systemd/system/node_exporter.service`, listening on localhost only. No firewall change.
2. Appended one line to the SSH user's `~/.ssh/authorized_keys` for the key `bm-monitor-tunnel`:
   `restrict,port-forwarding,permitopen="127.0.0.1:<port>",command="/usr/sbin/nologin" ssh-ed25519 …`.
   That key can only open a forward to the exporter: no shell, and any other destination is
   "administratively prohibited" (verified). On bm3, a backup of the previous file is at
   `~/.ssh/authorized_keys.bak-bm-monitor`.

**Undo on a server:**

```bash
sudo systemctl disable --now node_exporter
sudo rm /etc/systemd/system/node_exporter.service /usr/local/bin/node_exporter
sudo systemctl daemon-reload && sudo userdel node_exporter
sed -i '/bm-monitor-tunnel/d' ~/.ssh/authorized_keys
```

**When the dashboard moves to bm2** (production), drop the tunnels. Re-run the installer with
`NODE_EXPORTER_LISTEN=<wg ip>:9100` on each server, allow 9100 from `10.0.0.12` on wg0 only,
and register the servers as `http://10.0.0.1x:9100/metrics` with `ALLOWED_TARGET_CIDRS=10.0.0.0/24`.
