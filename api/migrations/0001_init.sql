-- Initial schema. All times are timestamptz (UTC), all ids are UUIDs.
-- Raw metric samples are NOT stored here; they live only in Prometheus.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE users (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email          text NOT NULL,
  name           text NOT NULL,
  password_hash  text,                                  -- argon2id PHC string; null for SSO-only users
  role           text NOT NULL CHECK (role IN ('admin','operator','viewer')),
  disabled       boolean NOT NULL DEFAULT false,
  failed_logins  int NOT NULL DEFAULT 0,
  locked_until   timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  last_login_at  timestamptz
);
CREATE UNIQUE INDEX users_email_lower ON users (lower(email));
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE sessions (
  token_hash    bytea PRIMARY KEY,                      -- sha256 of the random cookie token; the token itself is never stored
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,                   -- absolute expiry; idle expiry is checked against last_seen_at
  ip            inet,
  user_agent    text
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE server_groups (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name         text NOT NULL,
  description  text,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX server_groups_name_lower ON server_groups (lower(name));

CREATE TABLE servers (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name               text NOT NULL,
  hostname           text NOT NULL,
  exporter_url       text NOT NULL,                     -- as entered, e.g. http://10.0.0.11:9100/metrics
  target_addr        text NOT NULL,                     -- validated, resolved ip:port written to Prometheus (decision D1)
  environment        text NOT NULL DEFAULT 'production',
  location           text,
  group_id           uuid REFERENCES server_groups(id) ON DELETE SET NULL,
  enabled            boolean NOT NULL DEFAULT true,
  scrape_interval_s  int NOT NULL DEFAULT 30 CHECK (scrape_interval_s BETWEEN 10 AND 300),
  cpu_cores          int CHECK (cpu_cores > 0),         -- provisioned capacity: filled from first scrape, editable
  memory_bytes       bigint CHECK (memory_bytes > 0),
  disk_bytes         bigint CHECK (disk_bytes > 0),
  notes              text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  deleted_at         timestamptz                        -- soft delete keeps history and cost records
);
CREATE UNIQUE INDEX servers_exporter_live ON servers (exporter_url) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX servers_target_live   ON servers (target_addr)  WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX servers_name_live     ON servers (lower(name))  WHERE deleted_at IS NULL;
CREATE INDEX servers_group ON servers (group_id);
CREATE INDEX servers_env   ON servers (environment) WHERE deleted_at IS NULL;
CREATE TRIGGER servers_updated_at BEFORE UPDATE ON servers FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE server_labels (
  server_id  uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  key        text NOT NULL CHECK (key ~ '^[a-zA-Z_][a-zA-Z0-9_]{0,62}$'),
  value      text NOT NULL CHECK (length(value) <= 255),
  PRIMARY KEY (server_id, key)
);
CREATE INDEX server_labels_kv ON server_labels (key, value);

CREATE TABLE pricing_plans (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL,
  currency    char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount      numeric(14,4) NOT NULL CHECK (amount >= 0),
  unit        text NOT NULL CHECK (unit IN ('hour','month')),
  category    text NOT NULL CHECK (category IN ('compute','storage','network','license','fixed')),
  archived    boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- One primary plan per server at any instant (decision D2). Ranges are [from, to).
CREATE TABLE server_pricing (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  server_id       uuid NOT NULL REFERENCES servers(id),
  plan_id         uuid NOT NULL REFERENCES pricing_plans(id),
  effective_from  timestamptz NOT NULL,
  effective_to    timestamptz,                          -- null = still in effect
  created_by      uuid REFERENCES users(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  CHECK (effective_to IS NULL OR effective_to > effective_from),
  EXCLUDE USING gist (server_id WITH =, tstzrange(effective_from, coalesce(effective_to, 'infinity')) WITH &&)
);

-- Costs not covered by a server's primary plan: rack, bandwidth contract, licences,
-- or an extra per-server charge. Scope: server, group, or the whole fleet (both null).
CREATE TABLE fixed_costs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text NOT NULL,
  currency        char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount          numeric(14,4) NOT NULL CHECK (amount >= 0),
  unit            text NOT NULL CHECK (unit IN ('month','year')),
  category        text NOT NULL CHECK (category IN ('compute','storage','network','license','fixed')),
  server_id       uuid REFERENCES servers(id),
  group_id        uuid REFERENCES server_groups(id),
  allocation      text NOT NULL DEFAULT 'none' CHECK (allocation IN ('none','equal','cores')),
  effective_from  timestamptz NOT NULL,
  effective_to    timestamptz,
  created_by      uuid REFERENCES users(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  CHECK (NOT (server_id IS NOT NULL AND group_id IS NOT NULL)),
  CHECK (effective_to IS NULL OR effective_to > effective_from)
);

CREATE TABLE cost_records (                             -- one row per server per closed month
  server_id        uuid NOT NULL REFERENCES servers(id),
  period           date NOT NULL CHECK (extract(day FROM period) = 1),  -- first day of month, billing timezone
  kind             text NOT NULL CHECK (kind IN ('estimated','actual')),
  currency         char(3) NOT NULL,
  amount           numeric(14,2) NOT NULL,
  formula_version  int NOT NULL,
  source           text NOT NULL,                       -- 'engine', 'csv:<filename>', 'hetzner', ...
  details          jsonb NOT NULL,
  computed_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (server_id, period, kind)
);

CREATE TABLE alert_rules (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name             text NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('unreachable','cpu','memory','disk','disk_forecast','cost_increase')),
  threshold        numeric NOT NULL,                    -- percent; hours for disk_forecast; percent increase for cost_increase
  for_seconds      int NOT NULL DEFAULT 300 CHECK (for_seconds >= 0),
  severity         text NOT NULL CHECK (severity IN ('info','warning','critical')),
  scope_group_id   uuid REFERENCES server_groups(id),
  scope_server_id  uuid REFERENCES servers(id),
  enabled          boolean NOT NULL DEFAULT true,
  repeat_after_s   int NOT NULL DEFAULT 14400 CHECK (repeat_after_s >= 0),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CHECK (NOT (scope_group_id IS NOT NULL AND scope_server_id IS NOT NULL))
);
CREATE TRIGGER alert_rules_updated_at BEFORE UPDATE ON alert_rules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- An alert row is one incident (decision D8). At most one open row per fingerprint.
CREATE TABLE alerts (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  rule_id          uuid NOT NULL REFERENCES alert_rules(id),
  server_id        uuid REFERENCES servers(id),
  fingerprint      text NOT NULL,                       -- rule_id + server_id (+ mountpoint)
  labels           jsonb NOT NULL DEFAULT '{}',
  state            text NOT NULL CHECK (state IN ('pending','firing','acknowledged','resolved')),
  severity         text NOT NULL CHECK (severity IN ('info','warning','critical')),
  value            numeric,
  message          text NOT NULL,
  started_at       timestamptz NOT NULL,
  fired_at         timestamptz,
  acknowledged_at  timestamptz,
  acknowledged_by  uuid REFERENCES users(id),
  resolved_at      timestamptz,
  last_notified_at timestamptz,
  false_streak     int NOT NULL DEFAULT 0               -- consecutive checks where the condition was false
);
CREATE UNIQUE INDEX alerts_one_open ON alerts (fingerprint) WHERE state <> 'resolved';
CREATE INDEX alerts_state ON alerts (state, started_at DESC);
CREATE INDEX alerts_server ON alerts (server_id, started_at DESC);

CREATE TABLE notification_channels (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind          text NOT NULL CHECK (kind IN ('email','slack','telegram','discord')),
  name          text NOT NULL,
  config        jsonb NOT NULL,                         -- addresses only; secrets stay in env
  min_severity  text NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info','warning','critical')),
  enabled       boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
  id          bigserial PRIMARY KEY,
  alert_id    uuid NOT NULL REFERENCES alerts(id),
  channel_id  uuid NOT NULL REFERENCES notification_channels(id),
  event       text NOT NULL CHECK (event IN ('fired','reminder','resolved')),
  status      text NOT NULL CHECK (status IN ('queued','sent','failed')),
  attempts    int NOT NULL DEFAULT 0,
  next_try_at timestamptz NOT NULL DEFAULT now(),
  last_error  text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  sent_at     timestamptz
);
CREATE INDEX notifications_queue ON notifications (next_try_at) WHERE status = 'queued';

CREATE TABLE audit_events (
  id           bigserial PRIMARY KEY,
  actor_id     uuid REFERENCES users(id),
  action       text NOT NULL,                           -- server.create, server.disable, pricing.change ...
  target_type  text NOT NULL,
  target_id    text,
  before       jsonb,
  after        jsonb,
  ip           inet,
  request_id   text,
  at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_at ON audit_events (at DESC);
CREATE INDEX audit_target ON audit_events (target_type, target_id, at DESC);

CREATE TABLE settings (
  key         text PRIMARY KEY,
  value       jsonb NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id)
);

-- Defaults for configuration (not business data). Billing timezone and currency
-- are overwritten from env at start-up only if they are still unset.
INSERT INTO settings (key, value) VALUES
  ('rounding',               '{"mode":"half_up","decimals":2}'),
  ('proration',              '{"enabled":true}'),
  ('underutil_window_days',  '14'),
  ('underutil_cpu_p95',      '10'),
  ('underutil_mem_p95',      '30'),
  ('underutil_min_coverage', '80');

-- +goose Down
DROP TABLE settings;
DROP TABLE audit_events;
DROP TABLE notifications;
DROP TABLE notification_channels;
DROP TABLE alerts;
DROP TABLE alert_rules;
DROP TABLE cost_records;
DROP TABLE fixed_costs;
DROP TABLE server_pricing;
DROP TABLE pricing_plans;
DROP TABLE server_labels;
DROP TABLE servers;
DROP TABLE server_groups;
DROP TABLE sessions;
DROP TABLE users;
DROP FUNCTION set_updated_at();
