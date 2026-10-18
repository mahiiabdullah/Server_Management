-- Alerts go to people by group: a person tagged with a group gets email alerts
-- for the servers in that group. People can be dashboard users or alert-only
-- contacts, who can never sign in. "All alerts" people also get fleet-wide
-- alerts (monitoring down, cost increase), which belong to no group.

-- +goose Up
ALTER TABLE users ADD COLUMN kind text NOT NULL DEFAULT 'user' CHECK (kind IN ('user','contact'));
ALTER TABLE users ADD CONSTRAINT users_contact_no_login CHECK (kind = 'user' OR (password_hash IS NULL AND role = 'viewer'));
ALTER TABLE users ADD COLUMN alerts_all boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN alert_min_severity text NOT NULL DEFAULT 'warning'
  CHECK (alert_min_severity IN ('info','warning','critical'));
ALTER TABLE users ADD COLUMN alerts_paused_until timestamptz;   -- set by the person themselves

-- Who gets a group's alerts. Only admins add or remove rows; the person may mute.
CREATE TABLE group_alert_members (
  group_id    uuid NOT NULL REFERENCES server_groups(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  muted       boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_alert_members_user ON group_alert_members (user_id);

-- A team mailbox (notification channel) can be limited to some groups; none = every alert.
CREATE TABLE notification_channel_groups (
  channel_id  uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  group_id    uuid NOT NULL REFERENCES server_groups(id) ON DELETE CASCADE,
  PRIMARY KEY (channel_id, group_id)
);

-- A notification goes to a channel or to one person.
ALTER TABLE notifications ALTER COLUMN channel_id DROP NOT NULL;
ALTER TABLE notifications ADD COLUMN user_id uuid REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE notifications ADD CONSTRAINT notifications_one_target CHECK ((channel_id IS NULL) <> (user_id IS NULL));
CREATE INDEX notifications_alert ON notifications (alert_id);

-- +goose Down
DROP INDEX notifications_alert;
DELETE FROM notifications WHERE user_id IS NOT NULL;
ALTER TABLE notifications DROP CONSTRAINT notifications_one_target;
ALTER TABLE notifications DROP COLUMN user_id;
ALTER TABLE notifications ALTER COLUMN channel_id SET NOT NULL;
DROP TABLE notification_channel_groups;
DROP TABLE group_alert_members;
DELETE FROM users WHERE kind = 'contact';
ALTER TABLE users DROP COLUMN alerts_paused_until;
ALTER TABLE users DROP COLUMN alert_min_severity;
ALTER TABLE users DROP COLUMN alerts_all;
ALTER TABLE users DROP CONSTRAINT users_contact_no_login;
ALTER TABLE users DROP COLUMN kind;
