package httpapi

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"bm-monitor/api/internal/cost"
)

var categories = []string{"compute", "storage", "network", "license", "fixed"}

// formula explains every estimate in plain words (plan section 11.2).
func (a *API) formula(prorate bool) map[string]any {
	monthly := "rate × active hours ÷ hours in the month (prorated)"
	if !prorate {
		monthly = "full rate if the server was active at any time in the month (proration off)"
	}
	return map[string]any{
		"version":      cost.FormulaVersion,
		"timezone":     a.cfg.BillingTimezone.String(),
		"currency":     a.costs.Currency(),
		"rounding":     "Full precision during calculation; each total rounded once, half-up, to 2 decimals.",
		"active_hours": "Hours the server was enabled and not deleted, inside the pricing period.",
		"hourly_plan":  "rate × active hours",
		"monthly_plan": monthly,
		"fixed_cost":   "monthly amount × share of the month the cost was in effect (yearly amounts ÷ 12). Spread over servers only when an allocation rule (equal or by CPU cores) is set.",
		"projected":    "Cost to date plus the rest of the month, assuming servers and prices that are active now stay active.",
		"kind":         "estimated — calculated from your configured prices. 'actual' appears only from imported invoices.",
		"not_money":    "CPU and memory utilisation are never converted into money.",
	}
}

func (a *API) costSummary(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	y, m, err := a.costs.ParsePeriod(r.URL.Query().Get("period"), now)
	if err != nil {
		fail(w, r, err)
		return
	}
	period := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	configured, err := a.costs.Configured(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if !configured {
		writeJSON(w, http.StatusOK, map[string]any{"status": "not_configured", "period": period, "currency": a.costs.Currency(),
			"formula": a.formula(a.costs.Prorate(r.Context()))})
		return
	}
	res, err := a.costs.Month(r.Context(), y, m, now)
	if err != nil {
		fail(w, r, err)
		return
	}
	if g := r.URL.Query().Get("group"); g != "" {
		var servers []cost.ServerCost
		toDate, proj := decimal.Zero, decimal.Zero
		for _, s := range res.Servers {
			if s.GroupID == g {
				servers = append(servers, s)
			}
		}
		var groups []cost.GroupCost
		for _, gc := range res.Groups {
			if gc.GroupID == g {
				groups = append(groups, gc)
				toDate, proj = gc.ToDate, gc.Projected
			}
		}
		if servers == nil {
			servers = []cost.ServerCost{}
		}
		if groups == nil {
			groups = []cost.GroupCost{}
		}
		res.Servers, res.Groups, res.FleetShared, res.ToDate, res.Projected = servers, groups, []cost.Line{}, toDate, proj
	}
	// Actual amounts for this period, from imports.
	actual := map[string]decimal.Decimal{}
	rows, err := a.pool.Query(r.Context(), `SELECT server_id, amount FROM cost_records WHERE period = $1 AND kind = 'actual'`,
		time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	if err != nil {
		fail(w, r, err)
		return
	}
	actualTotal := decimal.Zero
	for rows.Next() {
		var id string
		var amt decimal.Decimal
		if err := rows.Scan(&id, &amt); err != nil {
			rows.Close()
			fail(w, r, err)
			return
		}
		actual[id] = amt
		actualTotal = actualTotal.Add(amt)
	}
	rows.Close()
	var actualOut any
	if len(actual) > 0 {
		actualOut = map[string]any{"total": actualTotal, "by_server": actual}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "configured", "currency": a.costs.Currency(), "kind": "estimated",
		"result": res, "actual": actualOut, "formula": a.formula(res.ProrationOn)})
}

func (a *API) costTrend(w http.ResponseWriter, r *http.Request) {
	n := 12
	if v := r.URL.Query().Get("months"); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 1 || x > 36 {
			fail(w, r, errValidation(map[string]string{"months": "Must be between 1 and 36."}))
			return
		}
		n = x
	}
	configured, err := a.costs.Configured(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if !configured {
		writeJSON(w, http.StatusOK, map[string]any{"status": "not_configured", "currency": a.costs.Currency(), "points": []any{}})
		return
	}
	pts, err := a.costs.Trend(r.Context(), n, time.Now())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "configured", "currency": a.costs.Currency(), "points": pts})
}

// ---- plans ----

func (a *API) listPlans(w http.ResponseWriter, r *http.Request) {
	p, err := a.costs.ListPlans(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": p, "currency": a.costs.Currency()})
}

type planRequest struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Amount      *decimal.Decimal `json:"amount"`
	Unit        *string          `json:"unit"`
	Category    *string          `json:"category"`
	Archived    *bool            `json:"archived"`
}

func (req *planRequest) validate(create bool) error {
	f := map[string]string{}
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if *req.Name == "" || len(*req.Name) > 80 {
			f["name"] = "Use 1–80 characters."
		}
	} else if create {
		f["name"] = "Required."
	}
	if req.Amount != nil {
		if req.Amount.IsNegative() || req.Amount.GreaterThan(decimal.NewFromInt(1_000_000_000)) || req.Amount.Exponent() < -4 {
			f["amount"] = "Must be between 0 and 1,000,000,000 with at most 4 decimals."
		}
	} else if create {
		f["amount"] = "Required."
	}
	if req.Unit != nil {
		if *req.Unit != "hour" && *req.Unit != "month" {
			f["unit"] = "Use hour or month."
		}
	} else if create {
		f["unit"] = "Required."
	}
	if req.Category != nil {
		if !contains(categories, *req.Category) {
			f["category"] = "Use compute, storage, network, license or fixed."
		}
	} else if create {
		req.Category = ptr("compute")
	}
	if len(f) > 0 {
		return errValidation(f)
	}
	return nil
}

func (a *API) savePlan(w http.ResponseWriter, r *http.Request, id string) {
	var req planRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if err := req.validate(id == ""); err != nil {
		fail(w, r, err)
		return
	}
	p, err := a.costs.SavePlan(r.Context(), id, cost.PlanInput{Name: req.Name, Description: req.Description, Amount: req.Amount,
		Unit: req.Unit, Category: req.Category, Archived: req.Archived}, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, p)
}

func (a *API) createPlan(w http.ResponseWriter, r *http.Request) { a.savePlan(w, r, "") }
func (a *API) updatePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	a.savePlan(w, r, id)
}

// ---- assignment ----

func (a *API) serverPricing(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	h, err := a.costs.ServerPricingHistory(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": h})
}

func (a *API) assignPricing(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	var req struct {
		PlanID        *string `json:"plan_id"`
		EffectiveFrom string  `json:"effective_from"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	from := time.Now().UTC().Truncate(time.Second)
	if req.EffectiveFrom != "" {
		t, ok := parseDate(req.EffectiveFrom, a.cfg.BillingTimezone)
		if !ok {
			fail(w, r, errValidation(map[string]string{"effective_from": "Use YYYY-MM-DD or an RFC 3339 time."}))
			return
		}
		from = t
	}
	plan := ""
	if req.PlanID != nil {
		if !uuidRe.MatchString(*req.PlanID) {
			fail(w, r, errValidation(map[string]string{"plan_id": "Pricing plan not found."}))
			return
		}
		plan = *req.PlanID
	}
	u := a.event(r)
	if err := a.costs.AssignPlan(r.Context(), id, plan, from, u.ActorID, u); err != nil {
		fail(w, r, err)
		return
	}
	h, err := a.costs.ServerPricingHistory(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": h})
}

// ---- fixed costs ----

func (a *API) listFixed(w http.ResponseWriter, r *http.Request) {
	f, err := a.costs.ListFixed(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": f, "currency": a.costs.Currency()})
}

type fixedRequest struct {
	Name          string          `json:"name"`
	Amount        decimal.Decimal `json:"amount"`
	Unit          string          `json:"unit"`
	Category      string          `json:"category"`
	ServerID      *string         `json:"server_id"`
	GroupID       *string         `json:"group_id"`
	Allocation    string          `json:"allocation"`
	EffectiveFrom string          `json:"effective_from"`
	EffectiveTo   *string         `json:"effective_to"`
}

func (a *API) saveFixed(w http.ResponseWriter, r *http.Request, id string) {
	var req fixedRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	f := map[string]string{}
	in := cost.FixedInput{Name: strings.TrimSpace(req.Name), Amount: req.Amount, Unit: req.Unit, Category: req.Category, Allocation: req.Allocation}
	if in.Name == "" || len(in.Name) > 80 {
		f["name"] = "Use 1–80 characters."
	}
	if in.Amount.IsNegative() || in.Amount.GreaterThan(decimal.NewFromInt(1_000_000_000)) {
		f["amount"] = "Must be between 0 and 1,000,000,000."
	}
	if in.Unit != "month" && in.Unit != "year" {
		f["unit"] = "Use month or year."
	}
	if !contains(categories, in.Category) {
		f["category"] = "Use compute, storage, network, license or fixed."
	}
	if in.Allocation == "" {
		in.Allocation = "none"
	}
	if !contains([]string{"none", "equal", "cores"}, in.Allocation) {
		f["allocation"] = "Use none, equal or cores."
	}
	for _, p := range []*string{req.ServerID, req.GroupID} {
		if p != nil && *p != "" && !uuidRe.MatchString(*p) {
			f["scope"] = "Server or group not found."
		}
	}
	if req.ServerID != nil && *req.ServerID != "" {
		in.ServerID = req.ServerID
		if in.Allocation != "none" {
			f["allocation"] = "A cost for one server cannot be allocated."
		}
	}
	if req.GroupID != nil && *req.GroupID != "" {
		in.GroupID = req.GroupID
	}
	if in.ServerID != nil && in.GroupID != nil {
		f["scope"] = "Choose a server or a group, not both."
	}
	if t, ok := parseDate(req.EffectiveFrom, a.cfg.BillingTimezone); ok {
		in.EffectiveFrom = t
	} else {
		f["effective_from"] = "Use YYYY-MM-DD."
	}
	if req.EffectiveTo != nil && *req.EffectiveTo != "" {
		if t, ok := parseDate(*req.EffectiveTo, a.cfg.BillingTimezone); ok && t.After(in.EffectiveFrom) {
			in.EffectiveTo = &t
		} else {
			f["effective_to"] = "Use YYYY-MM-DD, after the start date."
		}
	}
	if len(f) > 0 {
		fail(w, r, errValidation(f))
		return
	}
	ev := a.event(r)
	out, err := a.costs.SaveFixed(r.Context(), id, in, ev.ActorID, ev)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (a *API) createFixed(w http.ResponseWriter, r *http.Request) { a.saveFixed(w, r, "") }
func (a *API) updateFixed(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	a.saveFixed(w, r, id)
}

func (a *API) deleteFixed(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	if err := a.costs.DeleteFixed(r.Context(), id, a.event(r)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- import ----

func (a *API) importCosts(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		fail(w, r, errValidation(map[string]string{"file": "Upload a CSV file of at most 5 MB as form field \"file\"."}))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, r, errValidation(map[string]string{"file": "Upload a CSV file as form field \"file\"."}))
		return
	}
	defer file.Close()
	name := filepath.Base(hdr.Filename)
	if !strings.HasSuffix(strings.ToLower(name), ".csv") {
		fail(w, r, errValidation(map[string]string{"file": "Only .csv files are accepted."}))
		return
	}
	res, err := a.costs.ImportActual(r.Context(), file, name, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if len(res.Errors) > 0 {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, res)
}
