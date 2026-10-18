package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/db"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

// Rule is a configured alert rule.
type Rule struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Threshold     float64    `json:"threshold"`
	ForSeconds    int        `json:"for_seconds"`
	Severity      string     `json:"severity"`
	ScopeGroupID  *string    `json:"scope_group_id"`
	ScopeServerID *string    `json:"scope_server_id"`
	Enabled       bool       `json:"enabled"`
	RepeatAfterS  int        `json:"repeat_after_s"`
	System        bool       `json:"system"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

var severityRank = map[string]int{"info": 1, "warning": 2, "critical": 3}

// condition is one evaluated fingerprint that is currently true.
type condition struct {
	serverID string
	labels   map[string]string
	value    *float64
	message  string
}

type Evaluator struct {
	pool    *pgxpool.Pool
	prom    *metrics.Client
	inv     *inventory.Store
	costs   *cost.Store
	log     *slog.Logger
	now     func() time.Time
	costMu  sync.Mutex
	costAt  time.Time
	costVal *costCheck
}

type costCheck struct {
	projected, baseline decimal.Decimal
	ok                  bool
}

func NewEvaluator(pool *pgxpool.Pool, prom *metrics.Client, inv *inventory.Store, costs *cost.Store, log *slog.Logger) *Evaluator {
	return &Evaluator{pool: pool, prom: prom, inv: inv, costs: costs, log: log, now: time.Now}
}

func (e *Evaluator) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := e.Evaluate(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("alert evaluation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (e *Evaluator) rules(ctx context.Context) ([]Rule, error) {
	rows, err := e.pool.Query(ctx, `SELECT id, name, kind, threshold, for_seconds, severity, scope_group_id, scope_server_id,
		enabled, repeat_after_s, system FROM alert_rules WHERE enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Threshold, &r.ForSeconds, &r.Severity, &r.ScopeGroupID,
			&r.ScopeServerID, &r.Enabled, &r.RepeatAfterS, &r.System); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Evaluate runs every enabled rule once.
func (e *Evaluator) Evaluate(ctx context.Context) error {
	now := e.now().UTC()
	rules, err := e.rules(ctx)
	if err != nil {
		return err
	}
	snap, snapErr := e.prom.Snapshot(ctx)
	servers, err := e.inv.List(ctx)
	if err != nil {
		return err
	}
	var forecast []metrics.Sample
	forecastErr := error(nil)
	forecastHorizon := -1.0

	for _, r := range rules {
		var conds map[string]condition
		switch {
		case r.Kind == "monitoring":
			conds = map[string]condition{}
			if snapErr != nil {
				conds["monitoring"] = condition{message: "Monitoring unavailable: Prometheus cannot be queried, so server states are unknown."}
			}
		case snapErr != nil && r.Kind != "cost_increase":
			continue // freeze host rules while Prometheus is down: no false "unreachable" storm, no false recoveries
		case r.Kind == "disk_forecast":
			if forecastHorizon != r.Threshold {
				forecast, forecastErr = e.prom.DiskForecast(ctx, time.Duration(r.Threshold*float64(time.Hour)))
				forecastHorizon = r.Threshold
			}
			if forecastErr != nil {
				continue
			}
			conds = e.forecastConds(r, servers, forecast)
		case r.Kind == "cost_increase":
			c, ok := e.costCondition(ctx, r, now)
			if !ok {
				continue
			}
			conds = c
		default:
			conds = e.hostConds(r, servers, snap, now)
		}
		if err := e.apply(ctx, r, conds, now); err != nil {
			return fmt.Errorf("rule %s: %w", r.Name, err)
		}
	}
	return nil
}

func inScope(r Rule, s *inventory.Server) bool {
	if !s.Enabled {
		return false
	}
	if r.ScopeServerID != nil && *r.ScopeServerID != s.ID {
		return false
	}
	if r.ScopeGroupID != nil && !s.InGroup(*r.ScopeGroupID) {
		return false
	}
	return true
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

func (e *Evaluator) hostConds(r Rule, servers []*inventory.Server, snap *metrics.Snapshot, now time.Time) map[string]condition {
	out := map[string]condition{}
	for _, s := range servers {
		if !inScope(r, s) {
			continue
		}
		cur := snap.Servers[s.ID]
		fp := r.ID + ":" + s.ID
		switch r.Kind {
		case "unreachable":
			interval := time.Duration(s.ScrapeIntervalS) * time.Second
			grace := 3 * interval
			if now.Sub(s.CreatedAt) < grace+30*time.Second {
				continue // just registered: give the first scrapes time to land
			}
			switch {
			case cur != nil && cur.Up != nil && !*cur.Up:
				out[fp] = condition{serverID: s.ID, message: fmt.Sprintf("%s is unreachable: the last scrape failed.", s.Name)}
			case cur == nil || cur.LastScrape == nil:
				out[fp] = condition{serverID: s.ID, message: fmt.Sprintf("%s is unreachable: no metrics received yet.", s.Name)}
			case now.Sub(*cur.LastScrape) > grace:
				age := now.Sub(*cur.LastScrape).Round(time.Second)
				v := age.Seconds()
				out[fp] = condition{serverID: s.ID, value: &v, message: fmt.Sprintf("%s is unreachable: no metrics for %s.", s.Name, age)}
			}
		case "cpu":
			if cur != nil && cur.CPU != nil && *cur.CPU*100 > r.Threshold {
				v := *cur.CPU * 100
				out[fp] = condition{serverID: s.ID, value: &v, message: fmt.Sprintf("%s CPU at %s (threshold %.0f%%).", s.Name, pct(*cur.CPU), r.Threshold)}
			}
		case "memory":
			if cur != nil && cur.Memory != nil && *cur.Memory*100 > r.Threshold {
				v := *cur.Memory * 100
				out[fp] = condition{serverID: s.ID, value: &v, message: fmt.Sprintf("%s memory at %s (threshold %.0f%%).", s.Name, pct(*cur.Memory), r.Threshold)}
			}
		case "disk":
			if cur == nil {
				continue
			}
			for _, fs := range cur.Filesystems {
				if fs.UsedRatio*100 > r.Threshold {
					v := fs.UsedRatio * 100
					out[fp+":"+fs.Mountpoint] = condition{serverID: s.ID, value: &v, labels: map[string]string{"mountpoint": fs.Mountpoint},
						message: fmt.Sprintf("%s disk %s at %s (threshold %.0f%%).", s.Name, fs.Mountpoint, pct(fs.UsedRatio), r.Threshold)}
				}
			}
		}
	}
	return out
}

func (e *Evaluator) forecastConds(r Rule, servers []*inventory.Server, forecast []metrics.Sample) map[string]condition {
	byID := map[string]*inventory.Server{}
	for _, s := range servers {
		if inScope(r, s) {
			byID[s.ID] = s
		}
	}
	out := map[string]condition{}
	for _, f := range forecast {
		s := byID[f.Labels["server_id"]]
		if s == nil {
			continue
		}
		mount := f.Labels["mountpoint"]
		out[r.ID+":"+s.ID+":"+mount] = condition{serverID: s.ID, labels: map[string]string{"mountpoint": mount},
			message: fmt.Sprintf("%s disk %s is predicted to be full within %.0f hours (linear trend over the last 6 h).", s.Name, mount, r.Threshold)}
	}
	return out
}

// costCondition compares this month's projected total with the average of the last
// three months that had any cost. It is recomputed at most every 10 minutes.
func (e *Evaluator) costCondition(ctx context.Context, r Rule, now time.Time) (map[string]condition, bool) {
	e.costMu.Lock()
	defer e.costMu.Unlock()
	if e.costVal == nil || now.Sub(e.costAt) > 10*time.Minute {
		cc := &costCheck{}
		if configured, err := e.costs.Configured(ctx); err == nil && configured {
			cur := now.In(e.costs.Location())
			res, err1 := e.costs.Month(ctx, cur.Year(), cur.Month(), now)
			prev, err2 := e.costs.PreviousMonthTotals(ctx, 3, now)
			if err1 == nil && err2 == nil {
				sum, n := decimal.Zero, 0
				for _, p := range prev {
					if p.IsPositive() {
						sum, n = sum.Add(p), n+1
					}
				}
				if n > 0 {
					cc.projected, cc.baseline, cc.ok = res.Projected, sum.Div(decimal.NewFromInt(int64(n))), true
				}
			}
		}
		e.costVal, e.costAt = cc, now
	}
	if !e.costVal.ok {
		return nil, false
	}
	out := map[string]condition{}
	limit := e.costVal.baseline.Mul(decimal.NewFromFloat(1 + r.Threshold/100))
	if e.costVal.projected.GreaterThan(limit) {
		inc, _ := e.costVal.projected.Sub(e.costVal.baseline).Div(e.costVal.baseline).Mul(decimal.NewFromInt(100)).Float64()
		inc = math.Round(inc*10) / 10
		out[r.ID] = condition{value: &inc, message: fmt.Sprintf("Projected cost this month is %s %s, %.1f%% above the 3-month average of %s %s (threshold %.0f%%). Estimated.",
			e.costVal.projected.StringFixed(2), e.costs.Currency(), inc, e.costVal.baseline.StringFixed(2), e.costs.Currency(), r.Threshold)}
	}
	return out, true
}

type openRow struct {
	id string
	Open
}

func (e *Evaluator) apply(ctx context.Context, r Rule, conds map[string]condition, now time.Time) error {
	rows, err := e.pool.Query(ctx, `SELECT id, fingerprint, state, started_at, last_notified_at, false_streak
		FROM alerts WHERE rule_id = $1 AND state <> 'resolved'`, r.ID)
	if err != nil {
		return err
	}
	open := map[string]*openRow{}
	for rows.Next() {
		var o openRow
		var fp, state string
		if err := rows.Scan(&o.id, &fp, &state, &o.StartedAt, &o.LastNotifiedAt, &o.FalseStreak); err != nil {
			rows.Close()
			return err
		}
		o.State = State(state)
		open[fp] = &o
	}
	rows.Close()

	timing := Timing{For: time.Duration(r.ForSeconds) * time.Second, RepeatAfter: time.Duration(r.RepeatAfterS) * time.Second}
	fps := map[string]bool{}
	for fp := range conds {
		fps[fp] = true
	}
	for fp := range open {
		fps[fp] = true
	}
	for fp := range fps {
		c, cond := conds[fp]
		var o *Open
		if or := open[fp]; or != nil {
			o = &or.Open
		}
		act := Step(o, cond, timing, now)
		if act.Create || act.NewState != "" || act.Notify != "" || (o != nil && act.FalseStreak != o.FalseStreak) || cond {
			id := ""
			if or := open[fp]; or != nil {
				id = or.id
			}
			if err := e.persist(ctx, r, fp, id, c, cond, act, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Evaluator) persist(ctx context.Context, r Rule, fp, id string, c condition, cond bool, act Action, now time.Time) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if act.Create {
		labels, _ := db.JSONB(c.labels)
		if c.labels == nil {
			labels = "{}"
		}
		var firedAt any
		if act.NewState == Firing {
			firedAt = now
		}
		err := tx.QueryRow(ctx, `INSERT INTO alerts (rule_id, server_id, fingerprint, labels, state, severity, value, message, started_at, fired_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING RETURNING id`,
			r.ID, nullStr(c.serverID), fp, labels, string(act.NewState), r.Severity, c.value, c.message, now, firedAt).Scan(&id)
		if err == pgx.ErrNoRows {
			return nil // another evaluator won the race; the unique index kept one open alert
		}
		if err != nil {
			return err
		}
	} else {
		switch act.NewState {
		case Firing:
			_, err = tx.Exec(ctx, `UPDATE alerts SET state = 'firing', fired_at = $2, false_streak = 0 WHERE id = $1`, id, now)
		case Resolved:
			_, err = tx.Exec(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = $2, false_streak = $3 WHERE id = $1`, id, now, act.FalseStreak)
		default:
			if cond {
				_, err = tx.Exec(ctx, `UPDATE alerts SET value = $2, message = $3, false_streak = 0, severity = $4 WHERE id = $1`, id, c.value, c.message, r.Severity)
			} else {
				_, err = tx.Exec(ctx, `UPDATE alerts SET false_streak = $2 WHERE id = $1`, id, act.FalseStreak)
			}
		}
		if err != nil {
			return err
		}
	}
	if act.Notify != "" {
		if err := queue(ctx, tx, id, r.Severity, act.Notify); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET last_notified_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		e.log.Info("alert event", "rule", r.Name, "event", act.Notify, "fingerprint", fp, "message", strings.TrimSpace(c.message))
	}
	return tx.Commit(ctx)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
