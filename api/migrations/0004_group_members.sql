-- Servers can belong to any number of groups (one server can serve several
-- products, and a product runs on several servers). Replaces servers.group_id.
-- Membership is current state: past cost months use today's groups, as before.

-- +goose Up
CREATE TABLE server_group_members (
  server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  group_id    uuid NOT NULL REFERENCES server_groups(id) ON DELETE CASCADE,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (server_id, group_id)
);
CREATE INDEX server_group_members_group ON server_group_members (group_id);

INSERT INTO server_group_members (server_id, group_id)
SELECT id, group_id FROM servers WHERE group_id IS NOT NULL;

DROP INDEX servers_group;
ALTER TABLE servers DROP COLUMN group_id;

-- +goose Down
ALTER TABLE servers ADD COLUMN group_id uuid REFERENCES server_groups(id) ON DELETE SET NULL;
UPDATE servers s SET group_id = (
  SELECT m.group_id FROM server_group_members m JOIN server_groups g ON g.id = m.group_id
  WHERE m.server_id = s.id ORDER BY m.created_at, lower(g.name) LIMIT 1);
CREATE INDEX servers_group ON servers (group_id);
DROP TABLE server_group_members;
