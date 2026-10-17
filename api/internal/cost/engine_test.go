package cost

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var utc = time.UTC

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func at(y int, m time.Month, day, h int) time.Time { return time.Date(y, m, day, h, 0, 0, 0, utc) }

func eq(t *testing.T, what string, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(d(want)) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func server(id string, acts ...Interval) ServerInfo {
	return ServerInfo{ID: id, Name: id, Activity: acts}
}

func monthly(server string, amount string, from time.Time) Assignment {
	return Assignment{ServerID: server, PlanName: "m", Amount: d(amount), Unit: "month", Category: "compute", Interval: Interval{From: from}}
}

// October 2026 has 31 days = 744 hours.
func TestMonthlyFullMonth(t *testing.T) {
	s := server("a", Interval{From: at(2026, 9, 1, 0)})
	r := Compute(2026, 10, utc, at(2026, 11, 5, 0), true, []ServerInfo{s}, []Assignment{monthly("a", "100", at(2026, 1, 1, 0))}, nil)
	eq(t, "to date", r.ToDate, "100")
	eq(t, "projected", r.Projected, "100")
	if !r.Closed {
		t.Error("past month should be closed")
	}
}

func TestMonthlyProratedMidMonthStart(t *testing.T) {
	// Active from Oct 16 00:00 → 16 days = 384 h of 744 h. 100 × 384/744 = 51.6129... → 51.61
	s := server("a", Interval{From: at(2026, 10, 16, 0)})
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), true, []ServerInfo{s}, []Assignment{monthly("a", "100", at(2026, 1, 1, 0))}, nil)
	eq(t, "to date", r.ToDate, "51.61")
	eq(t, "active hours", r.Servers[0].ActiveHours, "384")
}

func TestProrationOffChargesFullMonth(t *testing.T) {
	s := server("a", Interval{From: at(2026, 10, 30, 0)})
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), false, []ServerInfo{s}, []Assignment{monthly("a", "100", at(2026, 1, 1, 0))}, nil)
	eq(t, "to date", r.ToDate, "100")
}

func TestHourlyWithDisableGap(t *testing.T) {
	// Active 10 h, disabled 5 h, active 5 h more → 15 h × 0.5 = 7.5
	s := server("a",
		Interval{From: at(2026, 10, 1, 0), To: at(2026, 10, 1, 10)},
		Interval{From: at(2026, 10, 1, 15), To: at(2026, 10, 1, 20)})
	a := Assignment{ServerID: "a", PlanName: "h", Amount: d("0.5"), Unit: "hour", Category: "compute", Interval: Interval{From: at(2026, 1, 1, 0)}}
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), true, []ServerInfo{s}, []Assignment{a}, nil)
	eq(t, "to date", r.ToDate, "7.5")
}

func TestPriceChangeMidMonth(t *testing.T) {
	// 100/month until Oct 11 00:00 (240 h), then 200/month (504 h).
	// 100×240/744 + 200×504/744 = 32.258... + 135.483... = 167.7419... → 167.74
	s := server("a", Interval{From: at(2026, 9, 1, 0)})
	a1 := monthly("a", "100", at(2026, 1, 1, 0))
	a1.To = at(2026, 10, 11, 0)
	a2 := monthly("a", "200", at(2026, 10, 11, 0))
	r := Compute(2026, 10, utc, at(2026, 12, 1, 0), true, []ServerInfo{s}, []Assignment{a1, a2}, nil)
	eq(t, "to date", r.ToDate, "167.74")
	if n := len(r.Servers[0].Lines); n != 2 {
		t.Fatalf("lines = %d, want 2 (one per price)", n)
	}
	// September keeps the old price only.
	sep := Compute(2026, 9, utc, at(2026, 12, 1, 0), true, []ServerInfo{s}, []Assignment{a1, a2}, nil)
	eq(t, "september", sep.ToDate, "100")
}

func TestProjectedCurrentMonth(t *testing.T) {
	// Now = Oct 11 00:00 (240 h of 744 elapsed). Server active all month, still active.
	s := server("a", Interval{From: at(2026, 9, 1, 0)})
	r := Compute(2026, 10, utc, at(2026, 10, 11, 0), true, []ServerInfo{s}, []Assignment{monthly("a", "744", at(2026, 1, 1, 0))}, nil)
	eq(t, "to date", r.ToDate, "240")
	eq(t, "projected", r.Projected, "744")
	eq(t, "hours elapsed", r.HoursElapsed, "240")
	if r.Closed {
		t.Error("current month must not be closed")
	}
}

func TestProjectionDoesNotExtrapolateStoppedServer(t *testing.T) {
	// Disabled on Oct 5: projected must equal to-date, not be scaled up to a full month.
	s := server("a", Interval{From: at(2026, 9, 1, 0), To: at(2026, 10, 5, 0)})
	r := Compute(2026, 10, utc, at(2026, 10, 20, 0), true, []ServerInfo{s}, []Assignment{monthly("a", "744", at(2026, 1, 1, 0))}, nil)
	eq(t, "to date", r.ToDate, "96")
	eq(t, "projected", r.Projected, "96")
}

func TestBillingTimezoneMonthEdges(t *testing.T) {
	dhaka := time.FixedZone("BDT", 6*3600)
	// In Dhaka, October starts at Sep 30 18:00 UTC. A server active only Sep 30 18:00–24:00 UTC
	// is in Dhaka's October (6 h), not in UTC's.
	s := server("a", Interval{From: time.Date(2026, 9, 30, 18, 0, 0, 0, utc), To: at(2026, 10, 1, 0)})
	a := Assignment{ServerID: "a", PlanName: "h", Amount: d("1"), Unit: "hour", Category: "compute", Interval: Interval{From: at(2026, 1, 1, 0)}}
	r := Compute(2026, 10, dhaka, at(2026, 12, 1, 0), true, []ServerInfo{s}, []Assignment{a}, nil)
	eq(t, "dhaka october", r.ToDate, "6")
	rUTC := Compute(2026, 10, utc, at(2026, 12, 1, 0), true, []ServerInfo{s}, []Assignment{a}, nil)
	eq(t, "utc october", rUTC.ToDate, "0")
}

func TestLeapYearFebruary(t *testing.T) {
	// Feb 2028 has 29 days = 696 h. Active 1 day: 696 × 24/696 = 24.
	s := server("a", Interval{From: time.Date(2028, 2, 10, 0, 0, 0, 0, utc), To: time.Date(2028, 2, 11, 0, 0, 0, 0, utc)})
	r := Compute(2028, 2, utc, at(2028, 4, 1, 0), true, []ServerInfo{s}, []Assignment{monthly("a", "696", at(2026, 1, 1, 0))}, nil)
	eq(t, "to date", r.ToDate, "24")
	eq(t, "hours in month", r.HoursInMonth, "696")
}

func TestRoundingHalfUpOnlyAtTheEnd(t *testing.T) {
	// Three servers at 0.335 each: rounding each line first would give 0.34×3 = 1.02;
	// rounding only the total gives 1.005 → 1.01.
	var servers []ServerInfo
	var assigns []Assignment
	for _, id := range []string{"a", "b", "c"} {
		servers = append(servers, server(id, Interval{From: at(2026, 10, 1, 0), To: at(2026, 10, 1, 1)}))
		assigns = append(assigns, Assignment{ServerID: id, PlanName: "h", Amount: d("0.335"), Unit: "hour", Category: "compute", Interval: Interval{From: at(2026, 1, 1, 0)}})
	}
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), true, servers, assigns, nil)
	eq(t, "fleet total", r.ToDate, "1.01")
	eq(t, "half-up of 0.005", Round(d("0.005")), "0.01")
}

func TestNoPricingIsReportedNotZeroGuessed(t *testing.T) {
	s := server("a", Interval{From: at(2026, 9, 1, 0)})
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), true, []ServerInfo{s}, nil, nil)
	if len(r.WithoutPricing) != 1 || r.WithoutPricing[0] != "a" {
		t.Fatalf("without pricing = %v", r.WithoutPricing)
	}
	if r.Servers[0].Priced {
		t.Error("server without plan marked priced")
	}
}

func TestInactiveServerIsNotListed(t *testing.T) {
	s := server("a", Interval{From: at(2026, 11, 3, 0)})
	r := Compute(2026, 10, utc, at(2026, 12, 1, 0), true, []ServerInfo{s}, []Assignment{monthly("a", "100", at(2026, 1, 1, 0))}, nil)
	if len(r.Servers) != 0 || len(r.WithoutPricing) != 0 {
		t.Fatalf("server inactive in October appears: %+v", r.Servers)
	}
}

func TestFixedCostsScopesAndAllocation(t *testing.T) {
	a := ServerInfo{ID: "a", Name: "a", GroupID: "g", GroupName: "G", Cores: 8, Activity: []Interval{{From: at(2026, 1, 1, 0)}}}
	b := ServerInfo{ID: "b", Name: "b", GroupID: "g", GroupName: "G", Cores: 24, Activity: []Interval{{From: at(2026, 1, 1, 0)}}}
	full := Interval{From: at(2026, 1, 1, 0)}
	fixed := []FixedCost{
		{Name: "rack", Amount: d("120"), Unit: "month", Category: "fixed", GroupID: "g", Allocation: "none", Interval: full},
		{Name: "licence", Amount: d("1200"), Unit: "year", Category: "license", Allocation: "cores", Interval: full}, // 100/month fleet, by cores
		{Name: "extra ip", Amount: d("5"), Unit: "month", Category: "network", ServerID: "b", Interval: full},
	}
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), true, []ServerInfo{a, b}, nil, fixed)
	eq(t, "fleet", r.ToDate, "225") // 120 + 100 + 5
	if len(r.Groups) != 1 {
		t.Fatalf("groups = %+v", r.Groups)
	}
	eq(t, "group", r.Groups[0].ToDate, "225") // rack (shared) + both servers
	var sa, sb ServerCost
	for _, s := range r.Servers {
		if s.ServerID == "a" {
			sa = s
		} else {
			sb = s
		}
	}
	eq(t, "server a (8/32 of licence)", sa.ToDate, "25")
	eq(t, "server b (24/32 of licence + ip)", sb.ToDate, "80")
	eq(t, "category license", r.ByCategory["license"], "100")
}

func TestFixedCostProratedByEffectiveDates(t *testing.T) {
	// A 744/month contract that starts on Oct 11 00:00 → 504 h → 504.
	fc := FixedCost{Name: "bw", Amount: d("744"), Unit: "month", Category: "network", Allocation: "none", Interval: Interval{From: at(2026, 10, 11, 0)}}
	r := Compute(2026, 10, utc, at(2026, 11, 1, 0), true, nil, nil, []FixedCost{fc})
	eq(t, "to date", r.ToDate, "504")
}
