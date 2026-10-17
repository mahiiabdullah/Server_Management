-- Server activity periods (enabled and not deleted), used by the cost engine to
-- know exactly which hours a server was billable. Maintained by the API on
-- create, enable, disable and delete.

-- +goose Up
CREATE TABLE server_activity (
  id           bigserial PRIMARY KEY,
  server_id    uuid NOT NULL REFERENCES servers(id),
  active_from  timestamptz NOT NULL,
  active_to    timestamptz,                             -- null = still active
  CHECK (active_to IS NULL OR active_to > active_from),
  EXCLUDE USING gist (server_id WITH =, tstzrange(active_from, coalesce(active_to, 'infinity')) WITH &&)
);
CREATE INDEX server_activity_server ON server_activity (server_id, active_from);

-- A system rule for "Prometheus itself is down", so one alert fires instead of
-- every server going unreachable at once.
ALTER TABLE alert_rules DROP CONSTRAINT alert_rules_kind_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_kind_check
  CHECK (kind IN ('unreachable','cpu','memory','disk','disk_forecast','cost_increase','monitoring'));
ALTER TABLE alert_rules ADD COLUMN system boolean NOT NULL DEFAULT false;

-- Pricing plans become immutable in amount/unit/currency once assigned; a price
-- change is a new plan assigned from a date. This keeps past periods stable.
ALTER TABLE pricing_plans ADD COLUMN description text;

-- Default rules. These are configuration, not data: all are editable or can be disabled.
INSERT INTO alert_rules (name, kind, threshold, for_seconds, severity, repeat_after_s, system) VALUES
  ('Monitoring unavailable',          'monitoring',    0,  60, 'critical', 14400, true),
  ('Server unreachable',              'unreachable',   0, 120, 'critical', 14400, false),
  ('High CPU',                        'cpu',          90, 600, 'warning',  14400, false),
  ('High memory',                     'memory',       90, 600, 'warning',  14400, false),
  ('Disk almost full',                'disk',         85, 300, 'warning',  14400, false),
  ('Disk full within 24 hours',       'disk_forecast',24, 900, 'critical', 14400, false),
  ('Monthly cost above 3-month average', 'cost_increase', 25, 0, 'warning', 86400, false);

-- +goose Down
-- Removes the seeded defaults (and their alert history) so a later Up does not duplicate them.
DELETE FROM notifications WHERE alert_id IN (SELECT a.id FROM alerts a JOIN alert_rules r ON r.id = a.rule_id
  WHERE r.name IN ('Monitoring unavailable','Server unreachable','High CPU','High memory','Disk almost full',
                   'Disk full within 24 hours','Monthly cost above 3-month average'));
DELETE FROM alerts WHERE rule_id IN (SELECT id FROM alert_rules
  WHERE name IN ('Monitoring unavailable','Server unreachable','High CPU','High memory','Disk almost full',
                 'Disk full within 24 hours','Monthly cost above 3-month average'));
DELETE FROM alert_rules
  WHERE name IN ('Monitoring unavailable','Server unreachable','High CPU','High memory','Disk almost full',
                 'Disk full within 24 hours','Monthly cost above 3-month average');
ALTER TABLE alert_rules DROP COLUMN system;
ALTER TABLE alert_rules DROP CONSTRAINT alert_rules_kind_check;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_kind_check
  CHECK (kind IN ('unreachable','cpu','memory','disk','disk_forecast','cost_increase'));
ALTER TABLE pricing_plans DROP COLUMN description;
DROP TABLE server_activity;
