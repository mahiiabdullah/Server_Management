package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/db"
)

var ErrNotFound = errors.New("not found")

// ConflictError reports a uniqueness clash on one field.
type ConflictError struct{ Field, Message string }

func (e *ConflictError) Error() string { return e.Field + ": " + e.Message }

type Server struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Hostname        string            `json:"hostname"`
	ExporterURL     string            `json:"exporter_url"`
	TargetAddr      string            `json:"target_addr"`
	Environment     string            `json:"environment"`
	Location        *string           `json:"location"`
	Groups          []GroupRef        `json:"groups"` // sorted by name
	Enabled         bool              `json:"enabled"`
	ScrapeIntervalS int               `json:"scrape_interval_s"`
	CPUCores        *int              `json:"cpu_cores"`
	MemoryBytes     *int64            `json:"memory_bytes"`
	DiskBytes       *int64            `json:"disk_bytes"`
	Notes           *string           `json:"notes"`
	Labels          map[string]string `json:"labels"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// GroupRef names one group a server belongs to.
type GroupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InGroup reports whether the server is a member of group id.
func (s *Server) InGroup(id string) bool {
	for _, g := range s.Groups {
		if g.ID == id {
			return true
		}
	}
	return false
}

type Group struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ServerCount int     `json:"server_count"`
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const serverCols = `
	s.id, s.name, s.hostname, s.exporter_url, s.target_addr, s.environment, s.location,
	coalesce((SELECT jsonb_agg(jsonb_build_object('id', g.id, 'name', g.name) ORDER BY lower(g.name))
	          FROM server_group_members m JOIN server_groups g ON g.id = m.group_id WHERE m.server_id = s.id), '[]'),
	s.enabled, s.scrape_interval_s, s.cpu_cores, s.memory_bytes, s.disk_bytes,
	s.notes, coalesce((SELECT jsonb_object_agg(l.key, l.value) FROM server_labels l WHERE l.server_id = s.id), '{}'),
	s.created_at, s.updated_at`

func scanServer(row pgx.Row) (*Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.Name, &s.Hostname, &s.ExporterURL, &s.TargetAddr, &s.Environment, &s.Location,
		&s.Groups, &s.Enabled, &s.ScrapeIntervalS, &s.CPUCores, &s.MemoryBytes, &s.DiskBytes,
		&s.Notes, &s.Labels, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// List returns every live (not deleted) server. Filtering, sorting and paging that
// depend on live metrics happen in the API layer after joining the metrics snapshot.
func (st *Store) List(ctx context.Context) ([]*Server, error) {
	rows, err := st.pool.Query(ctx, `SELECT `+serverCols+`
		FROM servers s
		WHERE s.deleted_at IS NULL ORDER BY lower(s.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (st *Store) Get(ctx context.Context, id string) (*Server, error) {
	s, err := scanServer(st.pool.QueryRow(ctx, `SELECT `+serverCols+`
		FROM servers s
		WHERE s.id = $1 AND s.deleted_at IS NULL`, id))
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return s, err
}

// Input is the writable part of a server. On update, nil fields are left unchanged.
type Input struct {
	Name            *string
	Hostname        *string
	Endpoint        *Endpoint
	Environment     *string
	Location        *string
	GroupIDs        *[]string // nil = unchanged; empty = no groups
	ScrapeIntervalS *int
	CPUCores        *int
	MemoryBytes     *int64
	DiskBytes       *int64
	Notes           *string
	Labels          map[string]string // nil = unchanged; empty = remove all
}

func conflict(err error) error {
	switch c, ok := db.IsUniqueViolation(err); {
	case !ok:
		return err
	case c == "servers_name_live":
		return &ConflictError{"name", "A server with this name already exists."}
	case c == "servers_exporter_live", c == "servers_target_live":
		return &ConflictError{"exporter_url", "Another server already uses this exporter address."}
	case c == "server_groups_name_lower":
		return &ConflictError{"name", "A group with this name already exists."}
	default:
		return err
	}
}

func nullable(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

// Create registers a server, opens its activity period if enabled, and audits it, in one transaction.
func (st *Store) Create(ctx context.Context, in Input, enabled bool, ev audit.Event) (*Server, error) {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO servers (name, hostname, exporter_url, target_addr, environment, location,
		                     enabled, scrape_interval_s, cpu_cores, memory_bytes, disk_bytes, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		*in.Name, *in.Hostname, in.Endpoint.CleanURL, in.Endpoint.TargetAddr, *in.Environment, nullable(in.Location),
		enabled, *in.ScrapeIntervalS, in.CPUCores, in.MemoryBytes, in.DiskBytes, nullable(in.Notes),
	).Scan(&id)
	if err != nil {
		return nil, conflict(err)
	}
	if err := replaceLabels(ctx, tx, id, in.Labels); err != nil {
		return nil, err
	}
	if err := replaceGroups(ctx, tx, id, in.GroupIDs); err != nil {
		return nil, err
	}
	if enabled {
		if _, err := tx.Exec(ctx, `INSERT INTO server_activity (server_id, active_from) VALUES ($1, now())`, id); err != nil {
			return nil, err
		}
	}
	s, err := scanServer(tx.QueryRow(ctx, `SELECT `+serverCols+` FROM servers s WHERE s.id = $1`, id))
	if err != nil {
		return nil, err
	}
	ev.Action, ev.TargetType, ev.TargetID, ev.After = "server.create", "server", id, s
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	return s, tx.Commit(ctx)
}

// replaceGroups sets the server's groups to exactly ids (nil leaves them unchanged).
func replaceGroups(ctx context.Context, tx pgx.Tx, id string, ids *[]string) error {
	if ids == nil {
		return nil
	}
	list := append([]string{}, *ids...) // never a nil slice: NULL would match nothing
	if _, err := tx.Exec(ctx, `DELETE FROM server_group_members WHERE server_id = $1 AND NOT (group_id = ANY($2::uuid[]))`, id, list); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO server_group_members (server_id, group_id) SELECT $1::uuid, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, id, list)
	return err
}

func replaceLabels(ctx context.Context, tx pgx.Tx, id string, labels map[string]string) error {
	if labels == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM server_labels WHERE server_id = $1`, id); err != nil {
		return err
	}
	for k, v := range labels {
		if _, err := tx.Exec(ctx, `INSERT INTO server_labels (server_id, key, value) VALUES ($1, $2, $3)`, id, k, v); err != nil {
			return err
		}
	}
	return nil
}

// Update applies the non-nil fields of in.
func (st *Store) Update(ctx context.Context, id string, in Input, ev audit.Event) (*Server, error) {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	before, err := scanServer(tx.QueryRow(ctx, `SELECT `+serverCols+` FROM servers s
		WHERE s.id = $1 AND s.deleted_at IS NULL FOR UPDATE OF s`, id))
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var exporterURL, targetAddr *string
	if in.Endpoint != nil {
		exporterURL, targetAddr = &in.Endpoint.CleanURL, &in.Endpoint.TargetAddr
	}
	_, err = tx.Exec(ctx, `
		UPDATE servers SET
		  name              = coalesce($2, name),
		  hostname          = coalesce($3, hostname),
		  exporter_url      = coalesce($4, exporter_url),
		  target_addr       = coalesce($5, target_addr),
		  environment       = coalesce($6, environment),
		  location          = CASE WHEN $7::boolean THEN $8 ELSE location END,
		  scrape_interval_s = coalesce($9, scrape_interval_s),
		  cpu_cores         = coalesce($10, cpu_cores),
		  memory_bytes      = coalesce($11, memory_bytes),
		  disk_bytes        = coalesce($12, disk_bytes),
		  notes             = CASE WHEN $13::boolean THEN $14 ELSE notes END
		WHERE id = $1`,
		id, in.Name, in.Hostname, exporterURL, targetAddr, in.Environment,
		in.Location != nil, nullable(in.Location),
		in.ScrapeIntervalS, in.CPUCores, in.MemoryBytes, in.DiskBytes, in.Notes != nil, nullable(in.Notes))
	if err != nil {
		return nil, conflict(err)
	}
	if err := replaceLabels(ctx, tx, id, in.Labels); err != nil {
		return nil, err
	}
	if err := replaceGroups(ctx, tx, id, in.GroupIDs); err != nil {
		return nil, err
	}
	after, err := scanServer(tx.QueryRow(ctx, `SELECT `+serverCols+` FROM servers s WHERE s.id = $1`, id))
	if err != nil {
		return nil, err
	}
	ev.Action, ev.TargetType, ev.TargetID, ev.Before, ev.After = "server.update", "server", id, before, after
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	return after, tx.Commit(ctx)
}

// SetEnabled starts or stops monitoring (and billing time) for a server.
func (st *Store) SetEnabled(ctx context.Context, id string, enabled bool, ev audit.Event) (*Server, error) {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var was bool
	err = tx.QueryRow(ctx, `SELECT enabled FROM servers WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&was)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if was != enabled {
		if _, err := tx.Exec(ctx, `UPDATE servers SET enabled = $2 WHERE id = $1`, id, enabled); err != nil {
			return nil, err
		}
		if err := setActivity(ctx, tx, id, enabled); err != nil {
			return nil, err
		}
		if !enabled {
			if err := resolveOpenAlerts(ctx, tx, id, "Server disabled"); err != nil {
				return nil, err
			}
		}
		ev.Action = map[bool]string{true: "server.enable", false: "server.disable"}[enabled]
		ev.TargetType, ev.TargetID = "server", id
		ev.Before, ev.After = map[string]bool{"enabled": was}, map[string]bool{"enabled": enabled}
		if err := audit.Record(ctx, tx, ev); err != nil {
			return nil, err
		}
	}
	s, err := scanServer(tx.QueryRow(ctx, `SELECT `+serverCols+` FROM servers s WHERE s.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return s, tx.Commit(ctx)
}

func setActivity(ctx context.Context, tx pgx.Tx, id string, active bool) error {
	if active {
		_, err := tx.Exec(ctx, `INSERT INTO server_activity (server_id, active_from) VALUES ($1, now())`, id)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE server_activity SET active_to = greatest(now(), active_from + interval '1 microsecond')
		WHERE server_id = $1 AND active_to IS NULL`, id)
	return err
}

func resolveOpenAlerts(ctx context.Context, tx pgx.Tx, serverID, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = now(), message = message || ' (' || $2 || ')'
		WHERE server_id = $1 AND state <> 'resolved'`, serverID, reason)
	return err
}

// Delete soft-deletes a server: history and cost records stay, monitoring and billing time stop.
func (st *Store) Delete(ctx context.Context, id string, ev audit.Event) error {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	before, err := scanServer(tx.QueryRow(ctx, `SELECT `+serverCols+` FROM servers s
		WHERE s.id = $1 AND s.deleted_at IS NULL FOR UPDATE OF s`, id))
	if db.IsNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE servers SET deleted_at = now(), enabled = false WHERE id = $1`, id); err != nil {
		return err
	}
	if before.Enabled {
		if err := setActivity(ctx, tx, id, false); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE server_pricing SET effective_to = now() WHERE server_id = $1 AND (effective_to IS NULL OR effective_to > now())
		AND effective_from < now()`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM server_pricing WHERE server_id = $1 AND effective_from >= now()`, id); err != nil {
		return err
	}
	if err := resolveOpenAlerts(ctx, tx, id, "Server removed"); err != nil {
		return err
	}
	ev.Action, ev.TargetType, ev.TargetID, ev.Before = "server.delete", "server", id, before
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FillCapacity stores observed capacity, but only for fields the user left empty.
func (st *Store) FillCapacity(ctx context.Context, id string, cores *int, mem, disk *int64) (bool, error) {
	tag, err := st.pool.Exec(ctx, `
		UPDATE servers SET
		  cpu_cores    = coalesce(cpu_cores, $2),
		  memory_bytes = coalesce(memory_bytes, $3),
		  disk_bytes   = coalesce(disk_bytes, $4)
		WHERE id = $1 AND deleted_at IS NULL
		  AND (cpu_cores IS NULL AND $2::int IS NOT NULL OR memory_bytes IS NULL AND $3::bigint IS NOT NULL
		       OR disk_bytes IS NULL AND $4::bigint IS NOT NULL)`, id, cores, mem, disk)
	return tag.RowsAffected() > 0, err
}

// ---- groups ----

func (st *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := st.pool.Query(ctx, `
		SELECT g.id, g.name, g.description, count(s.id)
		FROM server_groups g
		LEFT JOIN server_group_members m ON m.group_id = g.id
		LEFT JOIN servers s ON s.id = m.server_id AND s.deleted_at IS NULL
		GROUP BY g.id ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.ServerCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetGroup returns one group with its live server count.
func (st *Store) GetGroup(ctx context.Context, id string) (*Group, error) {
	var g Group
	err := st.pool.QueryRow(ctx, `
		SELECT g.id, g.name, g.description, count(s.id)
		FROM server_groups g
		LEFT JOIN server_group_members m ON m.group_id = g.id
		LEFT JOIN servers s ON s.id = m.server_id AND s.deleted_at IS NULL
		WHERE g.id = $1 GROUP BY g.id`, id).Scan(&g.ID, &g.Name, &g.Description, &g.ServerCount)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return &g, err
}

// GroupsExist reports whether every id is a group (ids must be distinct UUIDs).
func (st *Store) GroupsExist(ctx context.Context, ids []string) (bool, error) {
	var n int
	err := st.pool.QueryRow(ctx, `SELECT count(*) FROM server_groups WHERE id = ANY($1::uuid[])`, ids).Scan(&n)
	return n == len(ids), err
}

// SetGroupServers makes exactly serverIDs (live servers, distinct UUIDs) the
// members of group id. Deleted servers keep their past membership.
func (st *Store) SetGroupServers(ctx context.Context, id string, serverIDs []string, ev audit.Event) (*Group, error) {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	serverIDs = append([]string{}, serverIDs...) // never a nil slice: NULL would match nothing
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM server_groups WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
		if db.IsNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM servers WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL`, serverIDs).Scan(&live); err != nil {
		return nil, err
	}
	if live != len(serverIDs) {
		return nil, &ConflictError{"server_ids", "One or more servers do not exist."}
	}
	var before []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(m.server_id::text ORDER BY m.server_id), '{}') FROM server_group_members m
		JOIN servers s ON s.id = m.server_id AND s.deleted_at IS NULL WHERE m.group_id = $1`, id).Scan(&before); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM server_group_members m USING servers s
		WHERE m.group_id = $1 AND s.id = m.server_id AND s.deleted_at IS NULL AND NOT (m.server_id = ANY($2::uuid[]))`, id, serverIDs); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO server_group_members (server_id, group_id) SELECT unnest($2::uuid[]), $1::uuid ON CONFLICT DO NOTHING`, id, serverIDs); err != nil {
		return nil, err
	}
	ev.Action, ev.TargetType, ev.TargetID = "group.set_servers", "group", id
	ev.Before = map[string]any{"name": name, "server_ids": before}
	ev.After = map[string]any{"name": name, "server_ids": serverIDs}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return st.GetGroup(ctx, id)
}

func (st *Store) SaveGroup(ctx context.Context, id string, name string, description *string, ev audit.Event) (*Group, error) {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var g Group
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO server_groups (name, description) VALUES ($1, $2) RETURNING id, name, description`,
			name, nullable(description)).Scan(&g.ID, &g.Name, &g.Description)
		ev.Action = "group.create"
	} else {
		err = tx.QueryRow(ctx, `UPDATE server_groups SET name = $2, description = $3 WHERE id = $1 RETURNING id, name, description`,
			id, name, nullable(description)).Scan(&g.ID, &g.Name, &g.Description)
		ev.Action = "group.update"
	}
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, conflict(err)
	}
	ev.TargetType, ev.TargetID, ev.After = "group", g.ID, g
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	return &g, tx.Commit(ctx)
}

func (st *Store) DeleteGroup(ctx context.Context, id string, ev audit.Event) error {
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var name string
	if err := tx.QueryRow(ctx, `DELETE FROM server_groups WHERE id = $1 RETURNING name`, id).Scan(&name); err != nil {
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if db.IsForeignKeyViolation(err) {
			return &ConflictError{"id", "This group is still used by alert rules or fixed costs. Remove those first."}
		}
		return fmt.Errorf("delete group: %w", err)
	}
	ev.Action, ev.TargetType, ev.TargetID, ev.Before = "group.delete", "group", id, map[string]string{"name": name}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
