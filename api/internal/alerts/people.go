package alerts

// Alerts to people by group. A person (dashboard user or alert-only contact) is
// tagged with groups by an admin and gets email for alerts on servers in those
// groups. "All alerts" people get everything, including fleet-wide alerts that
// belong to no server. People may mute a group, lower their severity, or pause
// for a while; only admins change who is in a group.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/db"
)

const sevRankSQL = `CASE %s WHEN 'info' THEN 1 WHEN 'warning' THEN 2 ELSE 3 END`

func sevRank(col string) string { return strings.Replace(sevRankSQL, "%s", col, 1) }

// queue adds one notification per matching channel and per matching person for
// an alert event. It runs inside the evaluator's transaction.
func queue(ctx context.Context, tx pgx.Tx, alertID, severity, event string) error {
	rank := severityRank[severity]
	var server *string // nil for fleet-wide alerts
	if err := tx.QueryRow(ctx, `SELECT server_id::text FROM alerts WHERE id = $1`, alertID).Scan(&server); err != nil {
		return err
	}

	// Channels: enabled, severity high enough, and either unscoped or scoped to a group of the server.
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.kind, c.config FROM notification_channels c
		WHERE c.enabled AND `+sevRank("c.min_severity")+` <= $1
		  AND (NOT EXISTS (SELECT 1 FROM notification_channel_groups cg WHERE cg.channel_id = c.id)
		       OR EXISTS (SELECT 1 FROM notification_channel_groups cg
		                  JOIN server_group_members sm ON sm.group_id = cg.group_id
		                  WHERE cg.channel_id = c.id AND sm.server_id = $2::uuid))`, rank, server)
	if err != nil {
		return err
	}
	var channels []string
	covered := map[string]bool{} // addresses an email channel already reaches
	for rows.Next() {
		var id, kind string
		var cfg ChannelConfig
		if err := rows.Scan(&id, &kind, &cfg); err != nil {
			rows.Close()
			return err
		}
		channels = append(channels, id)
		if kind == "email" {
			for _, to := range cfg.To {
				covered[strings.ToLower(strings.TrimSpace(to))] = true
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// People. A resolution goes to whoever was told the alert fired.
	const active = `NOT u.disabled AND (u.alerts_paused_until IS NULL OR u.alerts_paused_until <= now())`
	if event == "resolved" {
		rows, err = tx.Query(ctx, `SELECT DISTINCT u.id, lower(u.email) FROM notifications n JOIN users u ON u.id = n.user_id
			WHERE n.alert_id = $1 AND n.event IN ('fired','reminder') AND `+active, alertID)
	} else {
		rows, err = tx.Query(ctx, `SELECT u.id, lower(u.email) FROM users u
			WHERE `+active+` AND `+sevRank("u.alert_min_severity")+` <= $1
			  AND (u.alerts_all OR EXISTS (SELECT 1 FROM group_alert_members gm
			        JOIN server_group_members sm ON sm.group_id = gm.group_id
			        WHERE gm.user_id = u.id AND NOT gm.muted AND sm.server_id = $2::uuid))`, rank, server)
	}
	if err != nil {
		return err
	}
	var people []string
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			rows.Close()
			return err
		}
		if !covered[email] { // never send the same alert twice to one address
			people = append(people, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if len(channels) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (alert_id, channel_id, event, status)
			SELECT $1, unnest($2::uuid[]), $3, 'queued'`, alertID, channels, event); err != nil {
			return err
		}
	}
	if len(people) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (alert_id, user_id, event, status)
			SELECT $1, unnest($2::uuid[]), $3, 'queued'`, alertID, people, event); err != nil {
			return err
		}
	}
	return nil
}

// ---- who is in which group ----

// PersonGroup is one group a person gets alerts for.
type PersonGroup struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Muted bool   `json:"muted"` // the person muted it themselves
}

// GroupsOfPeople returns every person's alert groups, by user id.
func (s *Store) GroupsOfPeople(ctx context.Context) (map[string][]PersonGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.user_id, g.id, g.name, m.muted FROM group_alert_members m
		JOIN server_groups g ON g.id = m.group_id ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]PersonGroup{}
	for rows.Next() {
		var uid string
		var g PersonGroup
		if err := rows.Scan(&uid, &g.ID, &g.Name, &g.Muted); err != nil {
			return nil, err
		}
		out[uid] = append(out[uid], g)
	}
	return out, rows.Err()
}

// PersonGroups returns one person's alert groups.
func (s *Store) PersonGroups(ctx context.Context, userID string) ([]PersonGroup, error) {
	all, err := s.GroupsOfPeople(ctx)
	if err != nil {
		return nil, err
	}
	if g := all[userID]; g != nil {
		return g, nil
	}
	return []PersonGroup{}, nil
}

// ErrUnknownGroup or ErrUnknownPerson: an id in a membership list does not exist.
var (
	ErrUnknownGroup  = errors.New("unknown group")
	ErrUnknownPerson = errors.New("unknown person")
)

// SetPersonGroups makes groupIDs (distinct UUIDs) exactly the person's alert
// groups. Groups the person keeps also keep their mute setting.
func (s *Store) SetPersonGroups(ctx context.Context, userID string, groupIDs []string, ev audit.Event) error {
	groupIDs = append([]string{}, groupIDs...)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM server_groups WHERE id = ANY($1::uuid[])`, groupIDs).Scan(&n); err != nil {
		return err
	}
	if n != len(groupIDs) {
		return ErrUnknownGroup
	}
	before, err := memberGroupIDs(ctx, tx, userID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM group_alert_members WHERE user_id = $1 AND NOT (group_id = ANY($2::uuid[]))`, userID, groupIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO group_alert_members (user_id, group_id) SELECT $1::uuid, unnest($2::uuid[]) ON CONFLICT DO NOTHING`,
		userID, groupIDs); err != nil {
		return err
	}
	if !sameSet(before, groupIDs) {
		ev.Action, ev.TargetType, ev.TargetID = "user.alert_groups", "user", userID
		ev.Before, ev.After = map[string]any{"group_ids": before}, map[string]any{"group_ids": groupIDs}
		if err := audit.Record(ctx, tx, ev); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func memberGroupIDs(ctx context.Context, tx pgx.Tx, userID string) ([]string, error) {
	var ids []string
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(group_id::text ORDER BY group_id), '{}') FROM group_alert_members WHERE user_id = $1`,
		userID).Scan(&ids)
	return ids, err
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

// GroupPerson is one person who gets a group's alerts.
type GroupPerson struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Email       string     `json:"email,omitempty"` // admins only
	Kind        string     `json:"kind"`
	Disabled    bool       `json:"disabled"`
	Muted       bool       `json:"muted"`
	MinSeverity string     `json:"min_severity"`
	PausedUntil *time.Time `json:"paused_until"`
}

func (s *Store) GroupPeople(ctx context.Context, groupID string) ([]GroupPerson, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.name, u.email, u.kind, u.disabled, m.muted, u.alert_min_severity,
		  CASE WHEN u.alerts_paused_until > now() THEN u.alerts_paused_until END
		FROM group_alert_members m JOIN users u ON u.id = m.user_id
		WHERE m.group_id = $1 ORDER BY u.disabled, lower(u.name)`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupPerson{}
	for rows.Next() {
		var p GroupPerson
		if err := rows.Scan(&p.ID, &p.Name, &p.Email, &p.Kind, &p.Disabled, &p.Muted, &p.MinSeverity, &p.PausedUntil); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetGroupPeople makes userIDs (distinct UUIDs) exactly the people who get a group's alerts.
func (s *Store) SetGroupPeople(ctx context.Context, groupID string, userIDs []string, ev audit.Event) error {
	userIDs = append([]string{}, userIDs...)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM server_groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&name); err != nil {
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = ANY($1::uuid[])`, userIDs).Scan(&n); err != nil {
		return err
	}
	if n != len(userIDs) {
		return ErrUnknownPerson
	}
	var before []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(user_id::text ORDER BY user_id), '{}') FROM group_alert_members WHERE group_id = $1`,
		groupID).Scan(&before); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM group_alert_members WHERE group_id = $1 AND NOT (user_id = ANY($2::uuid[]))`, groupID, userIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO group_alert_members (group_id, user_id) SELECT $1::uuid, unnest($2::uuid[]) ON CONFLICT DO NOTHING`,
		groupID, userIDs); err != nil {
		return err
	}
	ev.Action, ev.TargetType, ev.TargetID = "group.set_people", "group", groupID
	ev.Before, ev.After = map[string]any{"name": name, "user_ids": before}, map[string]any{"name": name, "user_ids": userIDs}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- a person's own settings ----

// OwnPrefs is what a person may see and change about their own alerts.
type OwnPrefs struct {
	All         bool          `json:"alerts_all"` // set by an admin
	MinSeverity string        `json:"min_severity"`
	PausedUntil *time.Time    `json:"paused_until"`
	Groups      []PersonGroup `json:"groups"` // membership set by an admin; mute is the person's
	// Team mailboxes that also send to this person's address; pausing does not stop them.
	Mailboxes []string `json:"mailboxes"`
}

func (s *Store) OwnPrefs(ctx context.Context, userID string) (*OwnPrefs, error) {
	var p OwnPrefs
	err := s.pool.QueryRow(ctx, `SELECT alerts_all, alert_min_severity, CASE WHEN alerts_paused_until > now() THEN alerts_paused_until END
		FROM users WHERE id = $1`, userID).Scan(&p.All, &p.MinSeverity, &p.PausedUntil)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.Groups, err = s.PersonGroups(ctx, userID); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(array_agg(c.name ORDER BY c.name), '{}') FROM notification_channels c, users u
		WHERE u.id = $1 AND c.kind = 'email' AND c.enabled
		  AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.config->'to') t WHERE lower(t) = lower(u.email))`, userID).Scan(&p.Mailboxes); err != nil {
		return nil, err
	}
	return &p, nil
}

// PrefsPatch changes a person's own settings; nil fields stay as they are.
type PrefsPatch struct {
	MinSeverity *string
	Pause       *time.Time // zero time = resume now
	Muted       map[string]bool
}

func (s *Store) UpdateOwnPrefs(ctx context.Context, userID string, p PrefsPatch, ev audit.Event) (*OwnPrefs, error) {
	before, err := s.OwnPrefs(ctx, userID)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if p.MinSeverity != nil {
		if _, err := tx.Exec(ctx, `UPDATE users SET alert_min_severity = $2 WHERE id = $1`, userID, *p.MinSeverity); err != nil {
			return nil, err
		}
	}
	if p.Pause != nil {
		var until any
		if !p.Pause.IsZero() {
			until = *p.Pause
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET alerts_paused_until = $2 WHERE id = $1`, userID, until); err != nil {
			return nil, err
		}
	}
	for gid, muted := range p.Muted {
		tag, err := tx.Exec(ctx, `UPDATE group_alert_members SET muted = $3 WHERE user_id = $1 AND group_id = $2`, userID, gid, muted)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrUnknownGroup // people can only mute groups an admin put them in
		}
	}
	after := *before
	if p.MinSeverity != nil {
		after.MinSeverity = *p.MinSeverity
	}
	if p.Pause != nil {
		after.PausedUntil = nil
		if !p.Pause.IsZero() {
			after.PausedUntil = p.Pause
		}
	}
	ev.Action, ev.TargetType, ev.TargetID, ev.Before = "user.alert_prefs", "user", userID, before
	ev.After = map[string]any{"min_severity": after.MinSeverity, "paused_until": after.PausedUntil, "muted": p.Muted}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.OwnPrefs(ctx, userID)
}

// ---- delivery log ----

// Delivery is one notification of an alert, for the alert's "sent to" list.
type Delivery struct {
	Event     string     `json:"event"`
	Target    string     `json:"target"` // person name and email, or channel name
	Kind      string     `json:"kind"`   // person | email | slack | telegram | discord
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	SentAt    *time.Time `json:"sent_at"`
	LastError *string    `json:"last_error"`
	CreatedAt time.Time  `json:"created_at"`
}

func (s *Store) Deliveries(ctx context.Context, alertID string) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.event, coalesce(u.name || ' <' || u.email || '>', c.name, '?'), CASE WHEN n.user_id IS NOT NULL THEN 'person' ELSE c.kind END,
		       n.status, n.attempts, n.sent_at, n.last_error, n.created_at
		FROM notifications n LEFT JOIN users u ON u.id = n.user_id LEFT JOIN notification_channels c ON c.id = n.channel_id
		WHERE n.alert_id = $1 ORDER BY n.created_at, n.id`, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.Event, &d.Target, &d.Kind, &d.Status, &d.Attempts, &d.SentAt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- signed pause links ----

// Links signs the pause and resume links in alert emails, so alert-only contacts
// (who cannot sign in) can pause their own alerts.
type Links struct{ key []byte }

// NewLinks derives the link key from the session secret.
func NewLinks(secret []byte) *Links {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("bm-monitor alert links v1"))
	return &Links{key: m.Sum(nil)}
}

// Link actions.
const (
	LinkPause1d = "pause1d"
	LinkPause7d = "pause7d"
	LinkResume  = "resume"
)

// LinkTTL is how long a link in an email keeps working.
const LinkTTL = 30 * 24 * time.Hour

var ErrBadLink = errors.New("this link is invalid or has expired")

func (l *Links) Sign(userID, action string, exp time.Time) string {
	payload := userID + "|" + action + "|" + strconv.FormatInt(exp.Unix(), 10)
	m := hmac.New(sha256.New, l.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify returns the person and action of a valid, unexpired link.
func (l *Links) Verify(token string, now time.Time) (userID, action string, err error) {
	p64, s64, ok := strings.Cut(token, ".")
	if !ok {
		return "", "", ErrBadLink
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p64)
	sig, err2 := base64.RawURLEncoding.DecodeString(s64)
	if err1 != nil || err2 != nil {
		return "", "", ErrBadLink
	}
	m := hmac.New(sha256.New, l.key)
	m.Write(payload)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return "", "", ErrBadLink
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 {
		return "", "", ErrBadLink
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", "", ErrBadLink
	}
	switch parts[1] {
	case LinkPause1d, LinkPause7d, LinkResume:
	default:
		return "", "", ErrBadLink
	}
	return parts[0], parts[1], nil
}

// PauseUntil is when a link action ends a pause (zero = resume now).
func PauseUntil(action string, now time.Time) time.Time {
	switch action {
	case LinkPause1d:
		return now.Add(24 * time.Hour)
	case LinkPause7d:
		return now.Add(7 * 24 * time.Hour)
	}
	return time.Time{}
}
