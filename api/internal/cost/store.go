package cost

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/db"
)

var ErrNotFound = errors.New("not found")

// ValidationError is a user-fixable problem in one field.
type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type Store struct {
	pool     *pgxpool.Pool
	loc      *time.Location
	currency string
	log      *slog.Logger
}

func NewStore(pool *pgxpool.Pool, loc *time.Location, currency string, log *slog.Logger) *Store {
	return &Store{pool: pool, loc: loc, currency: currency, log: log}
}

func (s *Store) Currency() string         { return s.currency }
func (s *Store) Location() *time.Location { return s.loc }

// ---- pricing plans ----

type Plan struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Currency    string          `json:"currency"`
	Amount      decimal.Decimal `json:"amount"`
	Unit        string          `json:"unit"`
	Category    string          `json:"category"`
	Archived    bool            `json:"archived"`
	InUse       int             `json:"servers_assigned"`
	Locked      bool            `json:"locked"` // amount/unit cannot change once ever assigned
	CreatedAt   time.Time       `json:"created_at"`
}

func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, p.description, p.currency, p.amount, p.unit, p.category, p.archived, p.created_at,
		  (SELECT count(DISTINCT sp.server_id) FROM server_pricing sp JOIN servers sv ON sv.id = sp.server_id
		   WHERE sp.plan_id = p.id AND sv.deleted_at IS NULL AND (sp.effective_to IS NULL OR sp.effective_to > now())),
		  EXISTS (SELECT 1 FROM server_pricing sp WHERE sp.plan_id = p.id)
		FROM pricing_plans p ORDER BY p.archived, lower(p.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Currency, &p.Amount, &p.Unit, &p.Category, &p.Archived, &p.CreatedAt, &p.InUse, &p.Locked); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type PlanInput struct {
	Name        *string
	Description *string
	Amount      *decimal.Decimal
	Unit        *string
	Category    *string
	Archived    *bool
}

func (s *Store) SavePlan(ctx context.Context, id string, in PlanInput, ev audit.Event) (*Plan, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var before *Plan
	if id != "" {
		var p Plan
		err := tx.QueryRow(ctx, `SELECT id, name, amount, unit, category, archived,
			EXISTS (SELECT 1 FROM server_pricing WHERE plan_id = $1) FROM pricing_plans WHERE id = $1 FOR UPDATE`, id).
			Scan(&p.ID, &p.Name, &p.Amount, &p.Unit, &p.Category, &p.Archived, &p.Locked)
		if db.IsNoRows(err) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		before = &p
		if p.Locked && ((in.Amount != nil && !in.Amount.Equal(p.Amount)) || (in.Unit != nil && *in.Unit != p.Unit) ||
			(in.Category != nil && *in.Category != p.Category)) {
			return nil, &ValidationError{"amount", "This plan has been assigned, so its price, unit and category are fixed to keep past costs correct. Create a new plan and assign it from a date instead."}
		}
	}
	var newID string
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO pricing_plans (name, description, currency, amount, unit, category)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			*in.Name, in.Description, s.currency, *in.Amount, *in.Unit, *in.Category).Scan(&newID)
		ev.Action = "pricing_plan.create"
	} else {
		newID = id
		_, err = tx.Exec(ctx, `UPDATE pricing_plans SET name = coalesce($2, name),
			description = CASE WHEN $3::boolean THEN $4 ELSE description END,
			amount = coalesce($5, amount), unit = coalesce($6, unit), category = coalesce($7, category),
			archived = coalesce($8, archived) WHERE id = $1`,
			id, in.Name, in.Description != nil, in.Description, in.Amount, in.Unit, in.Category, in.Archived)
		ev.Action = "pricing_plan.update"
	}
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := tx.QueryRow(ctx, `SELECT id, name, description, currency, amount, unit, category, archived, created_at FROM pricing_plans WHERE id = $1`, newID).
		Scan(&p.ID, &p.Name, &p.Description, &p.Currency, &p.Amount, &p.Unit, &p.Category, &p.Archived, &p.CreatedAt); err != nil {
		return nil, err
	}
	ev.TargetType, ev.TargetID, ev.After = "pricing_plan", p.ID, p
	if before != nil {
		ev.Before = before
	}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	return &p, tx.Commit(ctx)
}

// ---- server pricing assignments ----

type ServerPricing struct {
	ID            string          `json:"id"`
	PlanID        string          `json:"plan_id"`
	PlanName      string          `json:"plan_name"`
	Amount        decimal.Decimal `json:"amount"`
	Unit          string          `json:"unit"`
	Currency      string          `json:"currency"`
	Category      string          `json:"category"`
	EffectiveFrom time.Time       `json:"effective_from"`
	EffectiveTo   *time.Time      `json:"effective_to"`
}

func (s *Store) ServerPricingHistory(ctx context.Context, serverID string) ([]ServerPricing, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sp.id, p.id, p.name, p.amount, p.unit, p.currency, p.category, sp.effective_from, sp.effective_to
		FROM server_pricing sp JOIN pricing_plans p ON p.id = sp.plan_id
		WHERE sp.server_id = $1 ORDER BY sp.effective_from DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerPricing{}
	for rows.Next() {
		var p ServerPricing
		if err := rows.Scan(&p.ID, &p.PlanID, &p.PlanName, &p.Amount, &p.Unit, &p.Currency, &p.Category, &p.EffectiveFrom, &p.EffectiveTo); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AssignPlan puts planID on a server from `from` onwards. The assignment in effect at
// `from` is ended there; later (future) assignments are replaced. planID "" only ends pricing.
func (s *Store) AssignPlan(ctx context.Context, serverID, planID string, from time.Time, actorID string, ev audit.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var live bool
	if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM servers WHERE id = $1 FOR UPDATE`, serverID).Scan(&live); err != nil {
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	if !live {
		return ErrNotFound
	}
	if planID != "" {
		var archived bool
		var currency string
		err := tx.QueryRow(ctx, `SELECT archived, currency FROM pricing_plans WHERE id = $1`, planID).Scan(&archived, &currency)
		if db.IsNoRows(err) {
			return &ValidationError{"plan_id", "Pricing plan not found."}
		}
		if err != nil {
			return err
		}
		if archived {
			return &ValidationError{"plan_id", "This plan is archived."}
		}
		if currency != s.currency {
			return &ValidationError{"plan_id", fmt.Sprintf("Plan currency %s differs from the billing currency %s.", currency, s.currency)}
		}
	}
	// Remove assignments that start at or after `from`, then cut the one running at `from`.
	if _, err := tx.Exec(ctx, `DELETE FROM server_pricing WHERE server_id = $1 AND effective_from >= $2`, serverID, from); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE server_pricing SET effective_to = $2
		WHERE server_id = $1 AND effective_from < $2 AND (effective_to IS NULL OR effective_to > $2)`, serverID, from); err != nil {
		return err
	}
	if planID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO server_pricing (server_id, plan_id, effective_from, created_by) VALUES ($1, $2, $3, $4)`,
			serverID, planID, from, nullStr(actorID)); err != nil {
			return err
		}
	}
	ev.Action, ev.TargetType, ev.TargetID = "pricing.assign", "server", serverID
	ev.After = map[string]any{"plan_id": planID, "effective_from": from}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- fixed costs ----

type Fixed struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
	Unit          string          `json:"unit"`
	Category      string          `json:"category"`
	ServerID      *string         `json:"server_id"`
	ServerName    *string         `json:"server_name"`
	GroupID       *string         `json:"group_id"`
	GroupName     *string         `json:"group_name"`
	Allocation    string          `json:"allocation"`
	EffectiveFrom time.Time       `json:"effective_from"`
	EffectiveTo   *time.Time      `json:"effective_to"`
}

func (s *Store) ListFixed(ctx context.Context) ([]Fixed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.name, f.currency, f.amount, f.unit, f.category, f.server_id, sv.name, f.group_id, g.name,
		       f.allocation, f.effective_from, f.effective_to
		FROM fixed_costs f LEFT JOIN servers sv ON sv.id = f.server_id LEFT JOIN server_groups g ON g.id = f.group_id
		ORDER BY f.effective_to IS NOT NULL, lower(f.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fixed{}
	for rows.Next() {
		var f Fixed
		if err := rows.Scan(&f.ID, &f.Name, &f.Currency, &f.Amount, &f.Unit, &f.Category, &f.ServerID, &f.ServerName,
			&f.GroupID, &f.GroupName, &f.Allocation, &f.EffectiveFrom, &f.EffectiveTo); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

type FixedInput struct {
	Name          string
	Amount        decimal.Decimal
	Unit          string
	Category      string
	ServerID      *string
	GroupID       *string
	Allocation    string
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
}

func (s *Store) SaveFixed(ctx context.Context, id string, in FixedInput, actorID string, ev audit.Event) (*Fixed, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO fixed_costs (name, currency, amount, unit, category, server_id, group_id, allocation,
			effective_from, effective_to, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			in.Name, s.currency, in.Amount, in.Unit, in.Category, in.ServerID, in.GroupID, in.Allocation,
			in.EffectiveFrom, in.EffectiveTo, nullStr(actorID)).Scan(&id)
		ev.Action = "fixed_cost.create"
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = tx.Exec(ctx, `UPDATE fixed_costs SET name=$2, amount=$3, unit=$4, category=$5, server_id=$6, group_id=$7,
			allocation=$8, effective_from=$9, effective_to=$10 WHERE id=$1`,
			id, in.Name, in.Amount, in.Unit, in.Category, in.ServerID, in.GroupID, in.Allocation, in.EffectiveFrom, in.EffectiveTo)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, ErrNotFound
		}
		ev.Action = "fixed_cost.update"
	}
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return nil, &ValidationError{"scope", "Server or group not found."}
		}
		return nil, err
	}
	ev.TargetType, ev.TargetID, ev.After = "fixed_cost", id, in
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	all, err := s.ListFixed(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		if f.ID == id {
			return &f, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) DeleteFixed(ctx context.Context, id string, ev audit.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM fixed_costs WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	ev.Action, ev.TargetType, ev.TargetID = "fixed_cost.delete", "fixed_cost", id
	if err := audit.Record(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- loading inputs and computing ----

// Configured reports whether any price or fixed cost exists at all.
func (s *Store) Configured(ctx context.Context) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM server_pricing) OR EXISTS (SELECT 1 FROM fixed_costs)`).Scan(&ok)
	return ok, err
}

func (s *Store) Prorate(ctx context.Context) bool {
	var on bool
	if err := s.pool.QueryRow(ctx, `SELECT coalesce((SELECT (value->>'enabled')::boolean FROM settings WHERE key = 'proration'), true)`).Scan(&on); err != nil {
		return true
	}
	return on
}

// Load reads every input that can affect [from, to).
func (s *Store) Load(ctx context.Context, from, to time.Time) ([]ServerInfo, []Assignment, []FixedCost, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sv.id, sv.name, coalesce(sv.group_id::text, ''), coalesce(g.name, ''), coalesce(sv.cpu_cores, 0), sv.deleted_at IS NOT NULL,
		       a.active_from, a.active_to
		FROM server_activity a JOIN servers sv ON sv.id = a.server_id LEFT JOIN server_groups g ON g.id = sv.group_id
		WHERE a.active_from < $2 AND (a.active_to IS NULL OR a.active_to > $1)
		ORDER BY lower(sv.name), sv.id, a.active_from`, from, to)
	if err != nil {
		return nil, nil, nil, err
	}
	var servers []ServerInfo
	idx := map[string]int{}
	for rows.Next() {
		var si ServerInfo
		var iv Interval
		var toPtr *time.Time
		if err := rows.Scan(&si.ID, &si.Name, &si.GroupID, &si.GroupName, &si.Cores, &si.Deleted, &iv.From, &toPtr); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if toPtr != nil {
			iv.To = *toPtr
		}
		if i, ok := idx[si.ID]; ok {
			servers[i].Activity = append(servers[i].Activity, iv)
			continue
		}
		si.Activity = []Interval{iv}
		idx[si.ID] = len(servers)
		servers = append(servers, si)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	var assigns []Assignment
	rows, err = s.pool.Query(ctx, `
		SELECT sp.server_id, p.id, p.name, p.amount, p.unit, p.category, sp.effective_from, sp.effective_to
		FROM server_pricing sp JOIN pricing_plans p ON p.id = sp.plan_id
		WHERE sp.effective_from < $2 AND (sp.effective_to IS NULL OR sp.effective_to > $1)`, from, to)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var a Assignment
		var toPtr *time.Time
		if err := rows.Scan(&a.ServerID, &a.PlanID, &a.PlanName, &a.Amount, &a.Unit, &a.Category, &a.From, &toPtr); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if toPtr != nil {
			a.To = *toPtr
		}
		assigns = append(assigns, a)
	}
	rows.Close()

	var fixed []FixedCost
	rows, err = s.pool.Query(ctx, `
		SELECT id, name, amount, unit, category, coalesce(server_id::text, ''), coalesce(group_id::text, ''), allocation,
		       effective_from, effective_to
		FROM fixed_costs WHERE effective_from < $2 AND (effective_to IS NULL OR effective_to > $1)`, from, to)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fc FixedCost
		var toPtr *time.Time
		if err := rows.Scan(&fc.ID, &fc.Name, &fc.Amount, &fc.Unit, &fc.Category, &fc.ServerID, &fc.GroupID, &fc.Allocation, &fc.From, &toPtr); err != nil {
			return nil, nil, nil, err
		}
		if toPtr != nil {
			fc.To = *toPtr
		}
		fixed = append(fixed, fc)
	}
	return servers, assigns, fixed, rows.Err()
}

// Month computes one month (period "YYYY-MM" in the billing timezone).
func (s *Store) Month(ctx context.Context, year int, month time.Month, now time.Time) (*Result, error) {
	start, end := MonthBounds(year, month, s.loc)
	servers, assigns, fixed, err := s.Load(ctx, start, end)
	if err != nil {
		return nil, err
	}
	return Compute(year, month, s.loc, now, s.Prorate(ctx), servers, assigns, fixed), nil
}

// ParsePeriod accepts "YYYY-MM"; empty means the current month in the billing timezone.
func (s *Store) ParsePeriod(p string, now time.Time) (int, time.Month, error) {
	if p == "" {
		t := now.In(s.loc)
		return t.Year(), t.Month(), nil
	}
	t, err := time.ParseInLocation("2006-01", p, s.loc)
	if err != nil {
		return 0, 0, &ValidationError{"period", "Use the format YYYY-MM."}
	}
	return t.Year(), t.Month(), nil
}

// TrendPoint is one month of the cost history.
type TrendPoint struct {
	Period    string           `json:"period"`
	Estimated decimal.Decimal  `json:"estimated"`
	Projected *decimal.Decimal `json:"projected,omitempty"` // current month only
	Actual    *decimal.Decimal `json:"actual"`              // null unless an import or API supplied it
	Closed    bool             `json:"closed"`
}

// Trend returns the last n months, oldest first. Closed months use the stored
// estimate from the nightly closer when present.
func (s *Store) Trend(ctx context.Context, n int, now time.Time) ([]TrendPoint, error) {
	cur := now.In(s.loc)
	first := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, s.loc).AddDate(0, -(n - 1), 0)

	stored := map[string]map[string]decimal.Decimal{"estimated": {}, "actual": {}}
	rows, err := s.pool.Query(ctx, `SELECT to_char(period, 'YYYY-MM'), kind, sum(amount) FROM cost_records
		WHERE period >= $1 GROUP BY 1, 2`, first.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p, kind string
		var amt decimal.Decimal
		if err := rows.Scan(&p, &kind, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		stored[kind][p] = amt
	}
	rows.Close()

	out := make([]TrendPoint, 0, n)
	for i := 0; i < n; i++ {
		m := first.AddDate(0, i, 0)
		key := m.Format("2006-01")
		tp := TrendPoint{Period: key}
		if a, ok := stored["actual"][key]; ok {
			tp.Actual = &a
		}
		_, end := MonthBounds(m.Year(), m.Month(), s.loc)
		tp.Closed = !now.Before(end)
		if e, ok := stored["estimated"][key]; ok && tp.Closed {
			tp.Estimated = e
		} else {
			r, err := s.Month(ctx, m.Year(), m.Month(), now)
			if err != nil {
				return nil, err
			}
			tp.Estimated = r.ToDate
			if !tp.Closed {
				p := r.Projected
				tp.Projected = &p
			}
		}
		out = append(out, tp)
	}
	return out, nil
}

// CloseMonths writes estimated cost_records for closed months that have none yet,
// looking back up to `back` months. It is idempotent.
func (s *Store) CloseMonths(ctx context.Context, now time.Time, back int) (int, error) {
	cur := now.In(s.loc)
	written := 0
	for i := 1; i <= back; i++ {
		m := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, s.loc).AddDate(0, -i, 0)
		r, err := s.Month(ctx, m.Year(), m.Month(), now)
		if err != nil {
			return written, err
		}
		for _, sc := range r.Servers {
			if !sc.Priced && sc.ToDate.IsZero() {
				continue
			}
			details := map[string]any{"lines": sc.Lines, "active_hours": sc.ActiveHours, "proration": r.ProrationOn}
			raw, err := db.JSONB(details)
			if err != nil {
				return written, err
			}
			tag, err := s.pool.Exec(ctx, `INSERT INTO cost_records (server_id, period, kind, currency, amount, formula_version, source, details)
				VALUES ($1, $2, 'estimated', $3, $4, $5, 'engine', $6) ON CONFLICT DO NOTHING`,
				sc.ServerID, m.Format("2006-01-02"), s.currency, sc.ToDate, FormulaVersion, raw)
			if err != nil {
				return written, err
			}
			written += int(tag.RowsAffected())
		}
	}
	return written, nil
}

// RunCloser closes finished months once at start-up and then every 6 hours.
func (s *Store) RunCloser(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		if n, err := s.CloseMonths(ctx, time.Now(), 3); err != nil && ctx.Err() == nil {
			s.log.Error("cost closer failed", "err", err)
		} else if n > 0 {
			s.log.Info("closed-month cost records written", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ImportResult summarises a CSV import.
type ImportResult struct {
	Imported int      `json:"imported"`
	Errors   []string `json:"errors"`
}

// ImportActual reads a CSV of actual invoice amounts with the header
// server,period,amount[,currency] (server = name or id, period = YYYY-MM).
// Rows are validated first; nothing is written unless every row is valid.
func (s *Store) ImportActual(ctx context.Context, r io.Reader, filename string, ev audit.Event) (*ImportResult, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, &ValidationError{"file", "The file is empty or not CSV."}
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"server", "period", "amount"} {
		if _, ok := col[need]; !ok {
			return nil, &ValidationError{"file", "Header must contain server, period and amount (currency optional)."}
		}
	}
	type row struct {
		serverID string
		period   time.Time
		amount   decimal.Decimal
	}
	var good []row
	res := &ImportResult{Errors: []string{}}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		field := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		var id string
		err = s.pool.QueryRow(ctx, `SELECT id FROM servers WHERE id::text = $1 OR lower(name) = lower($1)
			ORDER BY deleted_at NULLS FIRST LIMIT 1`, field("server")).Scan(&id)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: unknown server %q", line, field("server")))
			continue
		}
		p, err := time.Parse("2006-01", field("period"))
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: period must be YYYY-MM", line))
			continue
		}
		amt, err := decimal.NewFromString(field("amount"))
		if err != nil || amt.IsNegative() {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: amount must be a non-negative number", line))
			continue
		}
		if c := strings.ToUpper(field("currency")); c != "" && c != s.currency {
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: currency %s differs from billing currency %s", line, c, s.currency))
			continue
		}
		good = append(good, row{id, p, amt})
	}
	if len(res.Errors) > 0 {
		return res, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	source := "csv:" + filename
	details, _ := db.JSONB(map[string]string{"source": source})
	for _, g := range good {
		if _, err := tx.Exec(ctx, `INSERT INTO cost_records (server_id, period, kind, currency, amount, formula_version, source, details)
			VALUES ($1, $2, 'actual', $3, $4, 0, $5, $6)
			ON CONFLICT (server_id, period, kind) DO UPDATE SET amount = EXCLUDED.amount, source = EXCLUDED.source,
			  details = EXCLUDED.details, computed_at = now()`,
			g.serverID, g.period.Format("2006-01-02"), s.currency, g.amount, source, details); err != nil {
			return nil, err
		}
	}
	ev.Action, ev.TargetType, ev.After = "costs.import", "cost_records", map[string]any{"file": filename, "rows": len(good)}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	res.Imported = len(good)
	return res, tx.Commit(ctx)
}

// FleetTotals returns the estimated fleet total for each of the n closed months
// before the current one (used by the cost-increase alert).
func (s *Store) PreviousMonthTotals(ctx context.Context, n int, now time.Time) ([]decimal.Decimal, error) {
	cur := now.In(s.loc)
	var out []decimal.Decimal
	for i := 1; i <= n; i++ {
		m := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, s.loc).AddDate(0, -i, 0)
		r, err := s.Month(ctx, m.Year(), m.Month(), now)
		if err != nil {
			return nil, err
		}
		out = append(out, r.ToDate)
	}
	return out, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
