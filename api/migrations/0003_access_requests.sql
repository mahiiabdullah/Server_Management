-- SSH access requests made through the dashboard. The source of truth for who
-- has access is access/grants.yaml in the GitHub repository (applied from bm2);
-- this table tracks each request's pull request and status for the dashboard.

-- +goose Up
CREATE SEQUENCE access_grant_seq START 1;

CREATE TABLE access_requests (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind            text NOT NULL CHECK (kind IN ('request','revoke')),
  grant_id        text NOT NULL,
  user_id         uuid REFERENCES users(id),             -- who asked (requester, or the admin for a revoke)
  requester_email text NOT NULL,                         -- the grant's owner
  unix_user       text NOT NULL,
  servers         text[] NOT NULL,
  sudo            boolean NOT NULL DEFAULT false,
  reason          text NOT NULL,
  key_fingerprint text,
  requested_at    timestamptz NOT NULL,
  expires_at      timestamptz,
  status          text NOT NULL CHECK (status IN ('pr_open','merged','active','rejected','expired','revoked','failed')),
  status_detail   text,
  branch          text NOT NULL,
  pr_number       int,
  pr_url          text,
  certificate     text,                                  -- public certificate, safe to show to its owner
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (kind, grant_id)
);
CREATE INDEX access_requests_user ON access_requests (user_id, created_at DESC);
CREATE INDEX access_requests_open ON access_requests (status) WHERE status IN ('pr_open','merged');
CREATE TRIGGER access_requests_updated_at BEFORE UPDATE ON access_requests FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE access_requests;
DROP SEQUENCE access_grant_seq;
