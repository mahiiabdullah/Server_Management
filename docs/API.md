# REST API contract

Base path `/api/v1`. JSON only (CSV upload is the one multipart endpoint). Times are
ISO 8601 UTC. Money is a JSON number with exact decimal digits, in the single billing
currency (`BILLING_CURRENCY`).

## Shared rules

**Errors**: always one shape, never a stack trace or internal address:

```json
{"error": {"code": "validation_failed", "message": "Some fields are invalid.", "fields": {"exporter_url": "address 169.254.169.254 is in a forbidden range (169.254.0.0/16)"}}}
```

| Status | Codes |
|--------|-------|
| 400 | `validation_failed`, `invalid_json` |
| 401 | `unauthenticated`, `invalid_credentials` |
| 403 | `forbidden` (role too low), `csrf_failed`, `bad_origin` |
| 404 | `not_found` (also for malformed ids) |
| 405 | `method_not_allowed` |
| 409 | `conflict` (duplicate name or endpoint), `system_rule`, `not_ackable`, `last_admin` |
| 413 / 415 | `body_too_large` (1 MiB JSON, 5 MB CSV), `unsupported_media_type` |
| 422 | `endpoint_unreachable` (registration probe failed), CSV rows invalid |
| 429 | `rate_limited` (per IP), `account_locked` (per account); both send `Retry-After` |
| 500 | `internal` (details are logged with the request id) |
| 503 | `metrics_unavailable` (Prometheus down); `/readyz` not ready |

**Lists**: `?page=1&page_size=25` (max 100) → `{"items": [...], "page": 1, "page_size": 25, "total": 137}`.

**Metrics**: a missing value is `null`, never `0`. History points are on a fixed grid,
and a missing sample is `[time, null]`.

**Auth**: the session cookie `bm_session` is HttpOnly, `SameSite=Lax`, and `Secure` on https.
It expires after 12 h idle or 7 days absolute. Every `POST/PUT/PATCH/DELETE` with a session
needs `X-CSRF-Token` (from the login or `/me` response, or the readable `bm_csrf` cookie).
A request whose `Origin` is not `PUBLIC_URL` is refused. There are no CORS headers, so only
the same-origin web app can call the API.

**Correlation**: every response carries `X-Request-Id`, and the same id is on the log line
and on audit events.

## Endpoints

Roles: **V** viewer, **O** operator, **A** admin (each includes the ones before it).

| Method and path | Role | Notes |
|---|---|---|
| `GET /healthz`, `GET /readyz` | – | Liveness; readiness checks PostgreSQL and Prometheus (2 s each) |
| `POST /auth/login` | – | `{email, password}` → `{user, csrf_token}`. 5 tries per IP per 15 min; an account locks for 15 min after 5 failures |
| `GET /auth/providers` | – | `{password: true, google: bool}` |
| `GET /auth/google`, `GET /auth/google/callback` | – | OpenID Connect with PKCE. Existing users only, or new viewers from `GOOGLE_ALLOWED_DOMAIN` |
| `POST /auth/logout` | V | Deletes the session server-side |
| `GET /me` | V | `{user, csrf_token}` |
| `POST /me/password` | V | `{current_password, new_password}` (≥12 chars); ends the user's other sessions |
| `GET /fleet/summary` | V | Status counts, core-weighted CPU, memory/disk by capacity, network, alert counts, cost or `not_configured` |
| `GET /servers` | V | `q`, `status` (csv), `environment`, `group` (id or `none`; a server in several groups matches each), `label=k=v`, `enabled`, `sort` (`name, status, cpu, memory, disk, uptime, last_scrape, cost, created_at, environment, group`, with `-` for descending; missing values always sort last), paging. Rows include live `metrics`, `status` and `monthly_cost` |
| `POST /servers/validate` | O | Checks and probes an exporter URL without saving; rate limited |
| `POST /servers` | O | Registers a server (see below); 201. `force: true` (admin only) saves an unreachable endpoint |
| `GET /servers/{id}` | V | Server, metrics with filesystems, pricing history, cost this month, open alerts, recent incidents |
| `PATCH /servers/{id}` | O | Any field; a changed `exporter_url` is checked and probed again |
| `POST /servers/{id}/enable`, `/disable` | O | Starts or stops monitoring and billing time; closes open incidents on disable |
| `DELETE /servers/{id}` | A | Body `{"confirm_name": "<exact name>"}`; soft delete that keeps history and cost records |
| `GET /servers/{id}/metrics` | V | `range=1h\|6h\|24h\|7d\|30d`, `series=cpu,memory,disk,network,load` → ≤ ~500 points per series, plus `coverage` |
| `GET /servers/{id}/pricing` · `PUT /servers/{id}/pricing` | V · A | `{plan_id or null, effective_from}`. The plan in effect then is ended; later ones are replaced |
| `GET /groups` | V | `{items:[{id, name, description, server_count}]}`; with `summary=1` each item adds `status` counts, `open_alerts` by severity, `cost_to_date`, `cost_projected`, `shared_servers` |
| `GET /groups/{id}` | V | `{group, servers (live, as in GET /servers), open_alerts, cost, currency, costs_configured}` |
| `POST /groups` · `PATCH /groups/{id}` | O | `{name, description}`; on create, optional `server_ids` as first members. Names cannot contain commas |
| `PUT /groups/{id}/servers` | O | `{server_ids:[…]}` replaces the group's members; servers stay in their other groups |
| `DELETE /groups/{id}` | A | Removes the group and its memberships; refused while alert rules or fixed costs use it |
| `GET /pricing-plans` · `POST`, `PATCH /pricing-plans/{id}` | V · A | Amount, unit and category are locked once a plan has been assigned |
| `GET /fixed-costs` · `POST`, `PATCH`, `DELETE /fixed-costs/{id}` | V · A | Scope server, group or fleet; allocation `none`, `equal` or `cores` |
| `GET /costs/summary` | V | `period=YYYY-MM`, `group=` → `{status:"not_configured"}` or `{result, actual, formula}` |
| `GET /costs/trend` | V | `months=1..36`. Estimated per month, projected for the current one, actual when imported |
| `POST /costs/import` | A | multipart `file` (.csv): `server,period,amount[,currency]`. All or nothing |
| `GET /alerts` | V | `state` (csv), `severity` (csv), `server`, paging |
| `POST /alerts/{id}/ack` | O | Firing → acknowledged; pending reminders are dropped |
| `GET /alert-rules` · `POST`, `PATCH`, `DELETE /alert-rules/{id}` | V · A | System rules cannot be deleted; rules with history are disabled rather than deleted |
| `GET`, `POST`, `PATCH /notification-channels` · `POST /notification-channels/{id}/test` | A | Addresses only; secrets stay in env |
| `GET /insights/underutilized` | V | Evidence for each server; savings only when the server is priced |
| `GET /insights/compare` | V | `metric=cpu\|memory\|disk&a=YYYY-MM&b=YYYY-MM&group=` |
| `GET /settings` · `PUT /settings` | V · A | Editable: `proration`, `underutil_window_days`, `underutil_cpu_p95`, `underutil_mem_p95`, `underutil_min_coverage`. Timezone and currency come from env |
| `GET /audit` | A | `action` (prefix), `target_type`, `target_id`, `actor`, paging |
| `GET /users` · `POST /users` · `PATCH /users/{id}` | A | Roles, disable, unlock, reset password. You cannot demote or disable yourself; at least one enabled admin always remains |

## Registering a server

`POST /api/v1/servers`

```json
{"name": "bm2", "exporter_url": "http://10.0.0.12:9100/metrics", "environment": "production",
 "group_ids": ["…", "…"], "location": "OVH GRA", "scrape_interval_s": 30, "labels": {"role": "worker"}}
```

A server can be in any number of groups. `group_ids` replaces the list on `PATCH` (`[]` removes all); leave it out to keep
the groups. Servers come back with `groups: [{id, name}]`, sorted by name. In cost results a server in several groups counts in
full in each group's total (the split rule is not decided yet); the fleet total counts it once.

1. The URL must be http(s) with no credentials, query or fragment, and a port in `ALLOWED_TARGET_PORTS`.
2. The host is resolved **once**. Every address must be inside `ALLOWED_TARGET_CIDRS`, and none may be in
   the always-denied ranges (loopback, link-local and cloud metadata, multicast, unspecified) or in `DENIED_TARGET_CIDRS`
   (the stack's own Docker networks).
3. A test fetch goes to that same IP: 3 s timeout, no redirects, 2 MiB limit, and the response must contain `node_exporter_build_info`.
4. A duplicate name (any case) or a duplicate endpoint among live servers gives `409`.
5. The server, its labels, its activity period and the audit event are written in one transaction. The resolved
   `ip:port` is written to Prometheus's targets file, and Prometheus picks it up without a reload.

`201`:

```json
{"server": {"id": "…", "name": "bm2", "target_addr": "10.0.0.12:9100", "…": "…"}, "status": "unknown",
 "validation": {"reachable": true, "node_exporter": true, "node_exporter_version": "1.9.1", "latency_ms": 41}}
```
