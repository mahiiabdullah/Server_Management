// Package cost turns configured prices into transparent cost estimates.
//
// Formulas (FormulaVersion 1), all in the billing timezone, full precision until the end:
//
//	active hours  = hours the server was enabled (not deleted) AND covered by a pricing assignment
//	hourly plan   = rate × active hours
//	monthly plan  = rate × active hours ÷ hours in the month          (proration on, default)
//	              = rate, if the server was active at any time in the month (proration off)
//	fixed cost    = monthly amount × (hours the cost was in effect ÷ hours in the month);
//	                yearly amounts count as amount ÷ 12 per month
//	to date       = the above up to now (or to month end for a past month)
//	projected     = the above to month end, assuming servers, prices and fixed costs that
//	                are active now stay active
//
// Totals are rounded once, half-up to 2 decimals. CPU or memory utilisation is never
// converted into money; fixed costs are only spread over servers by an explicit
// allocation rule (equal split, or by CPU cores).
package cost

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

const FormulaVersion = 1

// Interval is [From, To); a zero To means "still open".
type Interval struct {
	From time.Time
	To   time.Time
}

func (iv Interval) end(open time.Time) time.Time {
	if iv.To.IsZero() {
		return open
	}
	return iv.To
}

// Assignment is a pricing plan applied to a server over an interval.
type Assignment struct {
	ServerID string
	PlanID   string
	PlanName string
	Amount   decimal.Decimal
	Unit     string // hour | month
	Category string
	Interval
}

// FixedCost is a cost not covered by a server's plan.
type FixedCost struct {
	ID         string
	Name       string
	Amount     decimal.Decimal
	Unit       string // month | year
	Category   string
	ServerID   string // set: belongs to one server
	GroupID    string // set: belongs to a group
	Allocation string // none | equal | cores (for group or fleet scope)
	Interval
}

type ServerInfo struct {
	ID        string
	Name      string
	GroupID   string
	GroupName string
	Cores     int // 0 = unknown
	Deleted   bool
	Activity  []Interval
}

// Line is one explained component of a cost.
type Line struct {
	Source      string          `json:"source"` // plan | fixed
	Name        string          `json:"name"`
	Category    string          `json:"category"`
	Unit        string          `json:"unit"`
	Rate        decimal.Decimal `json:"rate"`
	ActiveHours decimal.Decimal `json:"active_hours"`
	Fraction    decimal.Decimal `json:"fraction"` // share of the month, or allocation share
	ToDate      decimal.Decimal `json:"to_date"`
	Projected   decimal.Decimal `json:"projected"`
	Note        string          `json:"note,omitempty"`
}

type ServerCost struct {
	ServerID    string          `json:"server_id"`
	Name        string          `json:"name"`
	GroupID     string          `json:"group_id,omitempty"`
	GroupName   string          `json:"group_name,omitempty"`
	Deleted     bool            `json:"deleted,omitempty"`
	Priced      bool            `json:"priced"`
	ActiveHours decimal.Decimal `json:"active_hours"`
	ToDate      decimal.Decimal `json:"to_date"`
	Projected   decimal.Decimal `json:"projected"`
	Lines       []Line          `json:"lines"`
}

type GroupCost struct {
	GroupID   string          `json:"group_id"`
	Name      string          `json:"name"`
	ToDate    decimal.Decimal `json:"to_date"`
	Projected decimal.Decimal `json:"projected"`
	Shared    []Line          `json:"shared"` // group fixed costs not allocated to servers
}

type Result struct {
	Period         string                     `json:"period"` // YYYY-MM
	Start          time.Time                  `json:"start"`
	End            time.Time                  `json:"end"`
	AsOf           time.Time                  `json:"as_of"`
	Closed         bool                       `json:"closed"`
	HoursInMonth   decimal.Decimal            `json:"hours_in_month"`
	HoursElapsed   decimal.Decimal            `json:"hours_elapsed"`
	ToDate         decimal.Decimal            `json:"to_date"`
	Projected      decimal.Decimal            `json:"projected"`
	ByCategory     map[string]decimal.Decimal `json:"by_category"` // to date
	ByCategoryProj map[string]decimal.Decimal `json:"by_category_projected"`
	Servers        []ServerCost               `json:"servers"`
	Groups         []GroupCost                `json:"groups"`
	FleetShared    []Line                     `json:"fleet_shared"`
	WithoutPricing []string                   `json:"servers_without_pricing"`
	FormulaVersion int                        `json:"formula_version"`
	ProrationOn    bool                       `json:"proration"`
}

// MonthBounds returns [start, end) of the month containing t, in loc, as UTC instants.
func MonthBounds(year int, month time.Month, loc *time.Location) (time.Time, time.Time) {
	s := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	return s.UTC(), s.AddDate(0, 1, 0).UTC()
}

var hourD = decimal.NewFromInt(3600)

func hours(d time.Duration) decimal.Decimal {
	if d <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(int64(d / time.Second)).Div(hourD)
}

// overlap returns the overlap of [a1,a2) and [b1,b2).
func overlap(a1, a2, b1, b2 time.Time) time.Duration {
	s, e := a1, a2
	if b1.After(s) {
		s = b1
	}
	if b2.Before(e) {
		e = b2
	}
	if !e.After(s) {
		return 0
	}
	return e.Sub(s)
}

// activeWithin sums the server's active time inside [from, to); open activity runs until openEnd.
func activeWithin(acts []Interval, from, to, openEnd time.Time) time.Duration {
	var d time.Duration
	for _, a := range acts {
		d += overlap(a.From, a.end(openEnd), from, to)
	}
	return d
}

// Compute calculates one month. asOf is "now"; for a past month it is ignored.
func Compute(year int, month time.Month, loc *time.Location, asOf time.Time, prorate bool,
	servers []ServerInfo, assigns []Assignment, fixed []FixedCost) *Result {

	start, end := MonthBounds(year, month, loc)
	cut := asOf
	if cut.After(end) {
		cut = end
	}
	if cut.Before(start) {
		cut = start
	}
	monthHours := hours(end.Sub(start))
	r := &Result{
		Period: time.Date(year, month, 1, 0, 0, 0, 0, loc).Format("2006-01"), Start: start, End: end, AsOf: asOf,
		Closed: !asOf.Before(end), HoursInMonth: monthHours, HoursElapsed: hours(cut.Sub(start)),
		ByCategory: map[string]decimal.Decimal{}, ByCategoryProj: map[string]decimal.Decimal{},
		FormulaVersion: FormulaVersion, ProrationOn: prorate, Servers: []ServerCost{}, Groups: []GroupCost{},
		FleetShared: []Line{}, WithoutPricing: []string{},
	}

	byServer := map[string]*ServerCost{}
	activeHrs := map[string]decimal.Decimal{} // projected active hours, for allocation
	var order []string
	for _, s := range servers {
		act := activeWithin(s.Activity, start, end, end)
		if act == 0 {
			continue // not active in this month: no cost, not listed
		}
		sc := &ServerCost{ServerID: s.ID, Name: s.Name, GroupID: s.GroupID, GroupName: s.GroupName, Deleted: s.Deleted,
			ActiveHours: hours(activeWithin(s.Activity, start, cut, cut)), Lines: []Line{}}
		byServer[s.ID] = sc
		activeHrs[s.ID] = hours(act)
		order = append(order, s.ID)
	}
	info := map[string]ServerInfo{}
	for _, s := range servers {
		info[s.ID] = s
	}

	// Plans.
	for _, a := range assigns {
		sc := byServer[a.ServerID]
		if sc == nil {
			continue
		}
		acts := info[a.ServerID].Activity
		aEnd := a.end(end)
		toDateH := hours(activeWithin(acts, maxT(a.From, start), minT(aEnd, cut), cut))
		projH := hours(activeWithin(acts, maxT(a.From, start), minT(aEnd, end), end))
		if projH.IsZero() && toDateH.IsZero() {
			continue
		}
		l := Line{Source: "plan", Name: a.PlanName, Category: a.Category, Unit: a.Unit, Rate: a.Amount, ActiveHours: toDateH}
		switch {
		case a.Unit == "hour":
			l.ToDate, l.Projected = a.Amount.Mul(toDateH), a.Amount.Mul(projH)
			l.Fraction = decimal.NewFromInt(1)
		case prorate:
			l.Fraction = projH.Div(monthHours)
			l.ToDate, l.Projected = a.Amount.Mul(toDateH).Div(monthHours), a.Amount.Mul(projH).Div(monthHours)
		default: // whole month if active at any time
			l.Fraction = decimal.NewFromInt(1)
			l.Projected = a.Amount
			if toDateH.IsPositive() {
				l.ToDate = a.Amount
			}
			l.Note = "proration off: full month charged"
		}
		sc.Priced = true
		sc.Lines = append(sc.Lines, l)
	}

	// Fixed costs.
	groups := map[string]*GroupCost{}
	groupOf := func(id string) *GroupCost {
		if groups[id] == nil {
			name := ""
			for _, s := range servers {
				if s.GroupID == id {
					name = s.GroupName
					break
				}
			}
			groups[id] = &GroupCost{GroupID: id, Name: name, Shared: []Line{}}
		}
		return groups[id]
	}
	for _, fc := range fixed {
		monthly := fc.Amount
		if fc.Unit == "year" {
			monthly = fc.Amount.Div(decimal.NewFromInt(12))
		}
		fEnd := fc.end(end)
		toDateFrac := hours(overlap(fc.From, minT(fEnd, cut), start, cut)).Div(monthHours)
		projFrac := hours(overlap(fc.From, fEnd, start, end)).Div(monthHours)
		if projFrac.IsZero() {
			continue
		}
		base := Line{Source: "fixed", Name: fc.Name, Category: fc.Category, Unit: fc.Unit, Rate: fc.Amount,
			Fraction: projFrac, ToDate: monthly.Mul(toDateFrac), Projected: monthly.Mul(projFrac)}

		if fc.ServerID != "" {
			if sc := byServer[fc.ServerID]; sc != nil {
				sc.Lines = append(sc.Lines, base)
			}
			continue
		}
		// Candidates for allocation: servers in scope that are active this month.
		var members []string
		for _, id := range order {
			if fc.GroupID == "" || info[id].GroupID == fc.GroupID {
				members = append(members, id)
			}
		}
		if fc.Allocation == "none" || len(members) == 0 {
			if fc.GroupID != "" {
				g := groupOf(fc.GroupID)
				g.Shared = append(g.Shared, base)
			} else {
				r.FleetShared = append(r.FleetShared, base)
			}
			continue
		}
		weights := map[string]decimal.Decimal{}
		total := decimal.Zero
		rule := fc.Allocation
		if rule == "cores" {
			for _, id := range members {
				if info[id].Cores <= 0 {
					rule = "equal" // cannot weigh by cores when one is unknown
					break
				}
			}
		}
		for _, id := range members {
			w := decimal.NewFromInt(1)
			if rule == "cores" {
				w = decimal.NewFromInt(int64(info[id].Cores))
			}
			weights[id], total = w, total.Add(w)
		}
		for _, id := range members {
			share := weights[id].Div(total)
			l := base
			l.Fraction = share
			l.ToDate, l.Projected = base.ToDate.Mul(share), base.Projected.Mul(share)
			l.Note = "allocated: " + rule
			if rule != fc.Allocation {
				l.Note += " (cores unknown for some servers)"
			}
			byServer[id].Lines = append(byServer[id].Lines, l)
		}
	}

	// Totals.
	add := func(m map[string]decimal.Decimal, k string, v decimal.Decimal) { m[k] = m[k].Add(v) }
	for _, id := range order {
		sc := byServer[id]
		for _, l := range sc.Lines {
			sc.ToDate, sc.Projected = sc.ToDate.Add(l.ToDate), sc.Projected.Add(l.Projected)
			add(r.ByCategory, l.Category, l.ToDate)
			add(r.ByCategoryProj, l.Category, l.Projected)
		}
		if !sc.Priced {
			r.WithoutPricing = append(r.WithoutPricing, id)
		}
		if sc.GroupID != "" {
			g := groupOf(sc.GroupID)
			g.ToDate, g.Projected = g.ToDate.Add(sc.ToDate), g.Projected.Add(sc.Projected)
		}
		r.ToDate, r.Projected = r.ToDate.Add(sc.ToDate), r.Projected.Add(sc.Projected)
	}
	for _, g := range groups {
		for _, l := range g.Shared {
			g.ToDate, g.Projected = g.ToDate.Add(l.ToDate), g.Projected.Add(l.Projected)
			r.ToDate, r.Projected = r.ToDate.Add(l.ToDate), r.Projected.Add(l.Projected)
			add(r.ByCategory, l.Category, l.ToDate)
			add(r.ByCategoryProj, l.Category, l.Projected)
		}
	}
	for _, l := range r.FleetShared {
		r.ToDate, r.Projected = r.ToDate.Add(l.ToDate), r.Projected.Add(l.Projected)
		add(r.ByCategory, l.Category, l.ToDate)
		add(r.ByCategoryProj, l.Category, l.Projected)
	}

	// Round once, at the end.
	for _, id := range order {
		sc := byServer[id]
		for i := range sc.Lines {
			roundLine(&sc.Lines[i])
		}
		sc.ToDate, sc.Projected, sc.ActiveHours = Round(sc.ToDate), Round(sc.Projected), sc.ActiveHours.Round(2)
		r.Servers = append(r.Servers, *sc)
	}
	for _, g := range groups {
		for i := range g.Shared {
			roundLine(&g.Shared[i])
		}
		g.ToDate, g.Projected = Round(g.ToDate), Round(g.Projected)
		r.Groups = append(r.Groups, *g)
	}
	for i := range r.FleetShared {
		roundLine(&r.FleetShared[i])
	}
	sort.Slice(r.Groups, func(i, j int) bool { return r.Groups[i].Name < r.Groups[j].Name })
	for k, v := range r.ByCategory {
		r.ByCategory[k] = Round(v)
	}
	for k, v := range r.ByCategoryProj {
		r.ByCategoryProj[k] = Round(v)
	}
	r.ToDate, r.Projected = Round(r.ToDate), Round(r.Projected)
	r.HoursElapsed = r.HoursElapsed.Round(2)
	return r
}

// Round rounds money half-up (away from zero) to 2 decimals.
func Round(d decimal.Decimal) decimal.Decimal { return d.Round(2) }

func roundLine(l *Line) {
	l.ToDate, l.Projected = Round(l.ToDate), Round(l.Projected)
	l.ActiveHours, l.Fraction = l.ActiveHours.Round(2), l.Fraction.Round(6)
}

func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minT(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func init() {
	// Money goes out as JSON numbers with exact decimal digits (e.g. 1840.5), not strings.
	decimal.MarshalJSONWithoutQuotes = true
}
