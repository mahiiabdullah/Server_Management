-- SSH access by group. A request can name access groups (defined in
-- access/policy.yaml) as well as servers; the grant then follows the group.
-- Dashboard groups are synced to access groups through reviewed policy pull
-- requests, tracked here.

-- +goose Up
ALTER TABLE access_requests ADD COLUMN groups text[] NOT NULL DEFAULT '{}';

CREATE TABLE access_policy_proposals (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  group_name      text NOT NULL,
  servers         text[],                                -- null: remove the group
  user_id         uuid REFERENCES users(id),
  requester_email text NOT NULL,
  branch          text NOT NULL,
  pr_number       int,
  pr_url          text,
  status          text NOT NULL CHECK (status IN ('pr_open','merged','rejected')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX access_policy_proposals_group ON access_policy_proposals (group_name, created_at DESC);
CREATE TRIGGER access_policy_proposals_updated_at BEFORE UPDATE ON access_policy_proposals FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE access_policy_proposals;
ALTER TABLE access_requests DROP COLUMN groups;
