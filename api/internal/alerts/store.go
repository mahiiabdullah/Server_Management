package alerts

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/db"
)

var ErrNotFound = errors.New("not found")

// ErrSystemRule is returned when deleting a built-in rule.
var ErrSystemRule = errors.New("system rules cannot be deleted; disable them instead")

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ---- rules ----

func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, kind, threshold, for_seconds, severity, scope_group_id, scope_server_id,
		enabled, repeat_after_s, system, updated_at FROM alert_rules ORDER BY system DESC, kind, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Threshold, &r.ForSeconds, &r.Severity, &r.ScopeGroupID,
			&r.ScopeServerID, &r.Enabled, &r.RepeatAfterS, &r.System, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRule(ctx context.Context, id string) (*Rule, error) {
	var r Rule
	err := s.pool.QueryRow(ctx, `SELECT id, name, kind, threshold, for_seconds, severity, scope_group_id, scope_server_id,
		enabled, repeat_after_s, system, updated_at FROM alert_rules WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.Kind, &r.Threshold, &r.ForSeconds, &r.Severity, &r.ScopeGroupID, &r.ScopeServerID,
			&r.Enabled, &r.RepeatAfterS, &r.System, &r.UpdatedAt)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return &r, err
}

// SaveRule creates (id == "") or replaces a rule. Kind cannot change on an existing rule.
func (s *Store) SaveRule(ctx context.Context, id string, r Rule, ev audit.Event) (*Rule, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO alert_rules (name, kind, threshold, for_seconds, severity, scope_group_id, scope_server_id, enabled, repeat_after_s)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
			r.Name, r.Kind, r.Threshold, r.ForSeconds, r.Severity, r.ScopeGroupID, r.ScopeServerID, r.Enabled, r.RepeatAfterS).Scan(&id)
		ev.Action = "alert_rule.create"
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = tx.Exec(ctx, `UPDATE alert_rules SET name=$2, threshold=$3, for_seconds=$4, severity=$5, scope_group_id=$6,
			scope_server_id=$7, enabled=$8, repeat_after_s=$9 WHERE id=$1`,
			id, r.Name, r.Threshold, r.ForSeconds, r.Severity, r.ScopeGroupID, r.ScopeServerID, r.Enabled, r.RepeatAfterS)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, ErrNotFound
		}
		ev.Action = "alert_rule.update"
	}
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return nil, errors.New("scope server or group not found")
		}
		return nil, err
	}
	// Disabling a rule closes its open incidents.
	if !r.Enabled {
		if _, err := tx.Exec(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = now() WHERE rule_id = $1 AND state <> 'resolved'`, id); err != nil {
			return nil, err
		}
	}
	r.ID = id
	ev.TargetType, ev.TargetID, ev.After = "alert_rule", id, r
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRule(ctx, id)
}

func (s *Store) DeleteRule(ctx context.Context, id string, ev audit.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var system bool
	if err := tx.QueryRow(ctx, `SELECT system FROM alert_rules WHERE id = $1 FOR UPDATE`, id).Scan(&system); err != nil {
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	if system {
		return ErrSystemRule
	}
	// Keep incident history: a rule with past alerts is disabled instead of deleted.
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alerts WHERE rule_id = $1)`, id).Scan(&used); err != nil {
		return err
	}
	if used {
		if _, err := tx.Exec(ctx, `UPDATE alert_rules SET enabled = false WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = now() WHERE rule_id = $1 AND state <> 'resolved'`, id); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1`, id); err != nil {
		return err
	}
	ev.Action, ev.TargetType, ev.TargetID = "alert_rule.delete", "alert_rule", id
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- alerts (incidents) ----

type Alert struct {
	ID             string            `json:"id"`
	RuleID         string            `json:"rule_id"`
	RuleName       string            `json:"rule_name"`
	Kind           string            `json:"kind"`
	ServerID       *string           `json:"server_id"`
	ServerName     *string           `json:"server_name"`
	Labels         map[string]string `json:"labels"`
	State          string            `json:"state"`
	Severity       string            `json:"severity"`
	Value          *float64          `json:"value"`
	Message        string            `json:"message"`
	StartedAt      time.Time         `json:"started_at"`
	FiredAt        *time.Time        `json:"fired_at"`
	AcknowledgedAt *time.Time        `json:"acknowledged_at"`
	AcknowledgedBy *string           `json:"acknowledged_by"`
	ResolvedAt     *time.Time        `json:"resolved_at"`
}

type Filter struct {
	States     []string
	Severities []string
	ServerID   string
	Page       int
	PageSize   int
}

func (s *Store) List(ctx context.Context, f Filter) ([]Alert, int, error) {
	// A condition that cleared while still pending never fired, so it is not an incident.
	where := []string{"NOT (a.state = 'resolved' AND a.fired_at IS NULL)"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}
	if len(f.States) > 0 {
		add("a.state = ANY(?)", f.States)
	}
	if len(f.Severities) > 0 {
		add("a.severity = ANY(?)", f.Severities)
	}
	if f.ServerID != "" {
		add("a.server_id = ?", f.ServerID)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM alerts a WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.rule_id, r.name, r.kind, a.server_id, sv.name, a.labels, a.state, a.severity, a.value, a.message,
		       a.started_at, a.fired_at, a.acknowledged_at, u.name, a.resolved_at
		FROM alerts a JOIN alert_rules r ON r.id = a.rule_id
		LEFT JOIN servers sv ON sv.id = a.server_id LEFT JOIN users u ON u.id = a.acknowledged_by
		WHERE `+w+`
		ORDER BY (a.state = 'resolved'), CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		         coalesce(a.resolved_at, a.started_at) DESC
		LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.Kind, &a.ServerID, &a.ServerName, &a.Labels, &a.State, &a.Severity,
			&a.Value, &a.Message, &a.StartedAt, &a.FiredAt, &a.AcknowledgedAt, &a.AcknowledgedBy, &a.ResolvedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// Counts returns open (not resolved, not pending) alerts by severity.
func (s *Store) Counts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{"critical": 0, "warning": 0, "info": 0}
	rows, err := s.pool.Query(ctx, `SELECT severity, count(*) FROM alerts WHERE state IN ('firing','acknowledged') GROUP BY severity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[sev] = n
	}
	return out, rows.Err()
}

var ErrNotAckable = errors.New("only firing alerts can be acknowledged")

func (s *Store) Acknowledge(ctx context.Context, id, userID string, ev audit.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM alerts WHERE id = $1 FOR UPDATE`, id).Scan(&state); err != nil {
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	if state != "firing" {
		return ErrNotAckable
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts SET state = 'acknowledged', acknowledged_at = now(), acknowledged_by = $2 WHERE id = $1`, id, userID); err != nil {
		return err
	}
	// Pending reminders for this alert are no longer wanted.
	if _, err := tx.Exec(ctx, `DELETE FROM notifications WHERE alert_id = $1 AND status = 'queued' AND event = 'reminder'`, id); err != nil {
		return err
	}
	ev.Action, ev.TargetType, ev.TargetID = "alert.acknowledge", "alert", id
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- notification channels ----

type Channel struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Name        string        `json:"name"`
	Config      ChannelConfig `json:"config"`
	MinSeverity string        `json:"min_severity"`
	Enabled     bool          `json:"enabled"`
	Configured  bool          `json:"configured"` // env provides the secrets this kind needs
	LastSent    *time.Time    `json:"last_sent_at"`
	LastError   *string       `json:"last_error"`
}

func (s *Store) ListChannels(ctx context.Context, senders *Senders) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.kind, c.name, c.config, c.min_severity, c.enabled,
		  (SELECT max(sent_at) FROM notifications n WHERE n.channel_id = c.id),
		  (SELECT last_error FROM notifications n WHERE n.channel_id = c.id AND n.last_error IS NOT NULL ORDER BY id DESC LIMIT 1)
		FROM notification_channels c ORDER BY c.kind, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.Config, &c.MinSeverity, &c.Enabled, &c.LastSent, &c.LastError); err != nil {
			return nil, err
		}
		c.Configured = senders.Available(c.Kind)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, id string) (*Channel, error) {
	var c Channel
	err := s.pool.QueryRow(ctx, `SELECT id, kind, name, config, min_severity, enabled FROM notification_channels WHERE id = $1`, id).
		Scan(&c.ID, &c.Kind, &c.Name, &c.Config, &c.MinSeverity, &c.Enabled)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Store) SaveChannel(ctx context.Context, id string, c Channel, ev audit.Event) (*Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	cfg, err := db.JSONB(c.Config)
	if err != nil {
		return nil, err
	}
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO notification_channels (kind, name, config, min_severity, enabled) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			c.Kind, c.Name, cfg, c.MinSeverity, c.Enabled).Scan(&id)
		ev.Action = "channel.create"
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = tx.Exec(ctx, `UPDATE notification_channels SET name=$2, config=$3, min_severity=$4, enabled=$5 WHERE id=$1`,
			id, c.Name, cfg, c.MinSeverity, c.Enabled)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, ErrNotFound
		}
		ev.Action = "channel.update"
	}
	if err != nil {
		return nil, err
	}
	ev.TargetType, ev.TargetID, ev.After = "notification_channel", id, c
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetChannel(ctx, id)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}
