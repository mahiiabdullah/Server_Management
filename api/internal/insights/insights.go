// Package insights finds barely used servers and compares utilisation between
// months. It only recommends; it never changes a server.
package insights

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

type Service struct {
	pool  *pgxpool.Pool
	prom  *metrics.Client
	inv   *inventory.Store
	costs *cost.Store
}

func New(pool *pgxpool.Pool, prom *metrics.Client, inv *inventory.Store, costs *cost.Store) *Service {
	return &Service{pool: pool, prom: prom, inv: inv, costs: costs}
}

// Rules are the configurable underutilisation thresholds (settings table).
type Rules struct {
	WindowDays  int     `json:"window_days"`
	CPUP95Max   float64 `json:"cpu_p95_max_pct"`
	MemP95Max   float64 `json:"memory_p95_max_pct"`
	MinCoverage float64 `json:"min_coverage_pct"`
}

func (s *Service) rules(ctx context.Context) Rules {
	r := Rules{WindowDays: 14, CPUP95Max: 10, MemP95Max: 30, MinCoverage: 80}
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings WHERE key LIKE 'underutil_%'`)
	if err != nil {
		return r
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var raw []byte
		if rows.Scan(&k, &raw) != nil {
			continue
		}
		var v float64
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		switch k {
		case "underutil_window_days":
			r.WindowDays = int(v)
		case "underutil_cpu_p95":
			r.CPUP95Max = v
		case "underutil_mem_p95":
			r.MemP95Max = v
		case "underutil_min_coverage":
			r.MinCoverage = v
		}
	}
	return r
}

// Evidence for one server over the window.
type Evidence struct {
	ServerID        string               `json:"server_id"`
	Name            string               `json:"name"`
	Groups          []inventory.GroupRef `json:"groups"`
	CPUP95          *float64             `json:"cpu_p95"`
	MemoryP95       *float64             `json:"memory_p95"`
	AvgCPU          *float64             `json:"avg_cpu"`
	AvgMemory       *float64             `json:"avg_memory"`
	Coverage        float64              `json:"coverage"`
	Underutilized   bool                 `json:"underutilized"`
	Reason          string               `json:"reason"`
	MonthlyCost     *decimal.Decimal     `json:"monthly_cost"`     // projected this month, only when priced
	PossibleSavings *decimal.Decimal     `json:"possible_savings"` // only when priced
	Recommendation  string               `json:"recommendation,omitempty"`
}

type Report struct {
	AsOf             time.Time        `json:"as_of"`
	WindowStart      time.Time        `json:"window_start"`
	Rules            Rules            `json:"rules"`
	Currency         string           `json:"currency"`
	Evaluated        int              `json:"evaluated"`
	InsufficientData int              `json:"insufficient_data"`
	Underutilized    []Evidence       `json:"underutilized"`
	Servers          []Evidence       `json:"servers"` // every enabled server, for the coverage table
	TotalSavings     *decimal.Decimal `json:"total_possible_savings"`
}

func (s *Service) Underutilized(ctx context.Context, now time.Time) (*Report, error) {
	rules := s.rules(ctx)
	window := time.Duration(rules.WindowDays) * 24 * time.Hour
	util, err := s.prom.UtilisationOver(ctx, window, now)
	if err != nil {
		return nil, err
	}
	servers, err := s.inv.List(ctx)
	if err != nil {
		return nil, err
	}
	costByServer := map[string]cost.ServerCost{}
	if configured, _ := s.costs.Configured(ctx); configured {
		cur := now.In(s.costs.Location())
		if res, err := s.costs.Month(ctx, cur.Year(), cur.Month(), now); err == nil {
			for _, sc := range res.Servers {
				costByServer[sc.ServerID] = sc
			}
		}
	}
	rep := &Report{AsOf: now, WindowStart: now.Add(-window), Rules: rules, Currency: s.costs.Currency(),
		Underutilized: []Evidence{}, Servers: []Evidence{}}
	total := decimal.Zero
	for _, sv := range servers {
		if !sv.Enabled {
			continue
		}
		rep.Evaluated++
		u := util[sv.ID]
		ev := Evidence{ServerID: sv.ID, Name: sv.Name, Groups: sv.Groups}
		expected := window.Seconds() / float64(sv.ScrapeIntervalS)
		if u != nil {
			ev.CPUP95, ev.MemoryP95, ev.AvgCPU, ev.AvgMemory = u.CPUP95, u.MemoryP95, u.AvgCPU, u.AvgMemory
			ev.Coverage = math.Min(1, u.UpSamples/expected)
		}
		if sc, ok := costByServer[sv.ID]; ok && sc.Priced {
			mc := sc.Projected
			ev.MonthlyCost = &mc
		}
		switch {
		case ev.Coverage*100 < rules.MinCoverage:
			rep.InsufficientData++
			ev.Reason = "Not enough data in the window to judge (coverage below the minimum)."
		case ev.CPUP95 == nil || ev.MemoryP95 == nil:
			rep.InsufficientData++
			ev.Reason = "CPU or memory series missing."
		case *ev.CPUP95*100 < rules.CPUP95Max && *ev.MemoryP95*100 < rules.MemP95Max:
			ev.Underutilized = true
			ev.Reason = "p95 CPU and p95 memory both below the thresholds for the whole window."
			ev.Recommendation = "Consider consolidating its workloads onto another server, or moving it to a smaller plan. Verify planned workloads and seasonal peaks first."
			if ev.MonthlyCost != nil {
				sv := *ev.MonthlyCost
				ev.PossibleSavings = &sv
				total = total.Add(sv)
			}
		default:
			ev.Reason = "Utilisation above the thresholds."
		}
		rep.Servers = append(rep.Servers, ev)
		if ev.Underutilized {
			rep.Underutilized = append(rep.Underutilized, ev)
		}
	}
	sort.Slice(rep.Underutilized, func(i, j int) bool {
		a, b := rep.Underutilized[i], rep.Underutilized[j]
		if (a.PossibleSavings == nil) != (b.PossibleSavings == nil) {
			return a.PossibleSavings != nil
		}
		if a.PossibleSavings != nil && !a.PossibleSavings.Equal(*b.PossibleSavings) {
			return a.PossibleSavings.GreaterThan(*b.PossibleSavings)
		}
		return *a.CPUP95 < *b.CPUP95
	})
	if !total.IsZero() {
		rep.TotalSavings = &total
	}
	return rep, nil
}

// CompareRow is one server's average in two months.
type CompareRow struct {
	ServerID string   `json:"server_id"`
	Name     string   `json:"name"`
	A        *float64 `json:"a"`
	B        *float64 `json:"b"`
	Delta    *float64 `json:"delta"` // B - A, in ratio points
}

// Compare returns each server's average CPU, memory or disk utilisation in two months.
func (s *Service) Compare(ctx context.Context, metric string, a, b time.Time, groupID string, now time.Time) ([]CompareRow, error) {
	avg := func(month time.Time) (map[string]*metrics.Utilisation, error) {
		start, end := cost.MonthBounds(month.Year(), month.Month(), s.costs.Location())
		if end.After(now) {
			end = now
		}
		if !end.After(start) {
			return map[string]*metrics.Utilisation{}, nil
		}
		return s.prom.UtilisationOver(ctx, end.Sub(start).Truncate(time.Minute), end)
	}
	ua, err := avg(a)
	if err != nil {
		return nil, err
	}
	ub, err := avg(b)
	if err != nil {
		return nil, err
	}
	pick := func(u *metrics.Utilisation) *float64 {
		if u == nil {
			return nil
		}
		switch metric {
		case "memory":
			return u.AvgMemory
		case "disk":
			return u.AvgDisk
		default:
			return u.AvgCPU
		}
	}
	servers, err := s.inv.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []CompareRow{}
	for _, sv := range servers {
		if groupID != "" && !sv.InGroup(groupID) {
			continue
		}
		r := CompareRow{ServerID: sv.ID, Name: sv.Name, A: pick(ua[sv.ID]), B: pick(ub[sv.ID])}
		if r.A != nil && r.B != nil {
			d := *r.B - *r.A
			r.Delta = &d
		}
		out = append(out, r)
	}
	return out, nil
}
