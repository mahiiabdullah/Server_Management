package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

var (
	nameRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	labelKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
)

// serverView is a server row joined with its live status, metrics and cost.
type serverView struct {
	*inventory.Server
	Status      string           `json:"status"`
	Metrics     *metrics.Current `json:"metrics"`
	MonthlyCost *decimal.Decimal `json:"monthly_cost"` // projected this month; null when unpriced
}

// costIndex maps server → projected monthly cost for priced servers (current month).
func (a *API) costIndex(ctx context.Context) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	now := time.Now()
	y, m, _ := a.costs.ParsePeriod("", now)
	res, err := a.costs.Month(ctx, y, m, now)
	if err != nil {
		loggerFrom(ctx).Warn("cost index failed", "err", err)
		return out
	}
	for _, s := range res.Servers {
		if s.Priced {
			out[s.ServerID] = s.Projected
		}
	}
	return out
}

func (a *API) views(ctx context.Context, servers []*inventory.Server) ([]*serverView, *metrics.Snapshot) {
	snap, err := a.snapshot(ctx)
	if err != nil {
		snap = nil
	}
	now := time.Now()
	costs := a.costIndex(ctx)
	out := make([]*serverView, 0, len(servers))
	for _, s := range servers {
		v := &serverView{Server: s}
		if snap != nil && s.Enabled {
			v.Metrics = snap.Servers[s.ID]
		}
		v.Status = inventory.Status(s, v.Metrics, now)
		if c, ok := costs[s.ID]; ok {
			c := c
			v.MonthlyCost = &c
		}
		if v.Metrics != nil {
			lite := *v.Metrics
			lite.Filesystems = nil // the list does not need per-mount detail
			v.Metrics = &lite
		}
		out = append(out, v)
	}
	return out, snap
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	page, size, err := paging(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	all, err := a.inv.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	views, snap := a.views(r.Context(), all)

	envs, groups := map[string]bool{}, map[string]string{}
	for _, v := range views {
		envs[v.Environment] = true
		if v.GroupID != nil {
			groups[*v.GroupID] = *v.GroupName
		}
	}

	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	statuses := splitCSV(q.Get("status"))
	var labelKey, labelVal string
	if l := q.Get("label"); l != "" {
		labelKey, labelVal, _ = strings.Cut(l, "=")
	}
	var filtered []*serverView
	for _, v := range views {
		switch {
		case text != "" && !strings.Contains(strings.ToLower(v.Name), text) && !strings.Contains(strings.ToLower(v.Hostname), text) &&
			!strings.Contains(v.TargetAddr, text):
		case q.Get("environment") != "" && v.Environment != q.Get("environment"):
		case q.Get("group") == "none" && v.GroupID != nil:
		case q.Get("group") != "" && q.Get("group") != "none" && (v.GroupID == nil || *v.GroupID != q.Get("group")):
		case len(statuses) > 0 && !contains(statuses, v.Status):
		case q.Get("enabled") == "true" && !v.Enabled, q.Get("enabled") == "false" && v.Enabled:
		case labelKey != "" && v.Labels[labelKey] != labelVal:
		default:
			filtered = append(filtered, v)
		}
	}
	if err := sortViews(filtered, q.Get("sort")); err != nil {
		fail(w, r, err)
		return
	}
	total := len(filtered)
	from := min((page-1)*size, total)
	to := min(from+size, total)

	envList := make([]string, 0, len(envs))
	for e := range envs {
		envList = append(envList, e)
	}
	sort.Strings(envList)
	type facetGroup struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	groupList := []facetGroup{}
	for id, name := range groups {
		groupList = append(groupList, facetGroup{id, name})
	}
	sort.Slice(groupList, func(i, j int) bool { return groupList[i].Name < groupList[j].Name })

	var asOf *time.Time
	if snap != nil {
		asOf = &snap.AsOf
	}
	items := filtered[from:to]
	if items == nil {
		items = []*serverView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "page": page, "page_size": size, "total": total,
		"as_of": asOf, "metrics_available": snap != nil,
		"facets": map[string]any{"environments": envList, "groups": groupList},
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var statusOrder = map[string]int{inventory.StatusOffline: 0, inventory.StatusUnknown: 1, inventory.StatusOnline: 2, inventory.StatusDisabled: 3}

// sortViews sorts by a key; "-" prefix = descending. Missing values always sort last.
func sortViews(v []*serverView, key string) error {
	desc := strings.HasPrefix(key, "-")
	key = strings.TrimPrefix(key, "-")
	if key == "" {
		key = "name"
	}
	num := func(get func(*serverView) *float64) func(a, b *serverView) (int, bool) {
		return func(a, b *serverView) (int, bool) {
			x, y := get(a), get(b)
			switch {
			case x == nil && y == nil:
				return 0, true
			case x == nil:
				return 1, false // nil last regardless of direction
			case y == nil:
				return -1, false
			case *x < *y:
				return -1, true
			case *x > *y:
				return 1, true
			}
			return 0, true
		}
	}
	metric := func(f func(*metrics.Current) *float64) func(*serverView) *float64 {
		return func(s *serverView) *float64 {
			if s.Metrics == nil {
				return nil
			}
			return f(s.Metrics)
		}
	}
	str := func(get func(*serverView) string) func(a, b *serverView) (int, bool) {
		return func(a, b *serverView) (int, bool) { return strings.Compare(get(a), get(b)), true }
	}
	var cmp func(a, b *serverView) (int, bool)
	switch key {
	case "name":
		cmp = str(func(s *serverView) string { return strings.ToLower(s.Name) })
	case "environment":
		cmp = str(func(s *serverView) string { return s.Environment })
	case "group":
		cmp = str(func(s *serverView) string {
			if s.GroupName == nil {
				return "￿"
			}
			return strings.ToLower(*s.GroupName)
		})
	case "status":
		cmp = func(a, b *serverView) (int, bool) { return statusOrder[a.Status] - statusOrder[b.Status], true }
	case "cpu":
		cmp = num(metric(func(c *metrics.Current) *float64 { return c.CPU }))
	case "memory":
		cmp = num(metric(func(c *metrics.Current) *float64 { return c.Memory }))
	case "disk":
		cmp = num(metric(func(c *metrics.Current) *float64 { return c.Disk }))
	case "uptime":
		cmp = num(metric(func(c *metrics.Current) *float64 { return c.UptimeSeconds }))
	case "last_scrape":
		cmp = num(metric(func(c *metrics.Current) *float64 {
			if c.LastScrape == nil {
				return nil
			}
			return ptr(float64(c.LastScrape.Unix()))
		}))
	case "cost":
		cmp = num(func(s *serverView) *float64 {
			if s.MonthlyCost == nil {
				return nil
			}
			f, _ := s.MonthlyCost.Float64()
			return &f
		})
	case "created_at":
		cmp = func(a, b *serverView) (int, bool) { return a.CreatedAt.Compare(b.CreatedAt), true }
	default:
		return errValidation(map[string]string{"sort": "Unknown sort key."})
	}
	sort.SliceStable(v, func(i, j int) bool {
		c, directional := cmp(v[i], v[j])
		if c == 0 {
			return strings.ToLower(v[i].Name) < strings.ToLower(v[j].Name)
		}
		if desc && directional {
			c = -c
		}
		return c < 0
	})
	return nil
}

// ---- create / validate / update ----

type serverRequest struct {
	Name            *string           `json:"name"`
	Hostname        *string           `json:"hostname"`
	ExporterURL     *string           `json:"exporter_url"`
	Environment     *string           `json:"environment"`
	Location        *string           `json:"location"`
	GroupID         *string           `json:"group_id"`
	ScrapeIntervalS *int              `json:"scrape_interval_s"`
	CPUCores        *int              `json:"cpu_cores"`
	MemoryBytes     *int64            `json:"memory_bytes"`
	DiskBytes       *int64            `json:"disk_bytes"`
	Notes           *string           `json:"notes"`
	Labels          map[string]string `json:"labels"`
	Enabled         *bool             `json:"enabled"`
	Force           bool              `json:"force"`
}

// check validates fields and builds the store input. create=true requires name and exporter_url.
func (a *API) checkServer(ctx context.Context, req *serverRequest, create bool) (inventory.Input, *inventory.Endpoint, error) {
	in := inventory.Input{Labels: req.Labels}
	fields := map[string]string{}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if !nameRe.MatchString(n) {
			fields["name"] = "Use 1–64 letters, digits, dots, dashes or underscores, starting with a letter or digit."
		}
		in.Name = &n
	} else if create {
		fields["name"] = "Required."
	}
	if req.Environment != nil {
		e := strings.ToLower(strings.TrimSpace(*req.Environment))
		if !envRe.MatchString(e) {
			fields["environment"] = "Use 1–32 lowercase letters, digits or dashes."
		}
		in.Environment = &e
	} else if create {
		in.Environment = ptr("production")
	}
	if req.Hostname != nil {
		h := strings.TrimSpace(*req.Hostname)
		if len(h) > 253 {
			fields["hostname"] = "Too long."
		}
		in.Hostname = &h
	}
	if req.Location != nil {
		l := strings.TrimSpace(*req.Location)
		if len(l) > 128 {
			fields["location"] = "At most 128 characters."
		}
		in.Location = &l
	}
	if req.Notes != nil {
		n := strings.TrimSpace(*req.Notes)
		if len(n) > 2000 {
			fields["notes"] = "At most 2000 characters."
		}
		in.Notes = &n
	}
	if req.ScrapeIntervalS != nil {
		if *req.ScrapeIntervalS < 10 || *req.ScrapeIntervalS > 300 {
			fields["scrape_interval_s"] = "Must be between 10 and 300 seconds."
		}
		in.ScrapeIntervalS = req.ScrapeIntervalS
	} else if create {
		in.ScrapeIntervalS = ptr(int(a.cfg.DefaultScrapeInterval.Seconds()))
	}
	if req.CPUCores != nil && (*req.CPUCores < 1 || *req.CPUCores > 4096) {
		fields["cpu_cores"] = "Must be between 1 and 4096."
	}
	if req.MemoryBytes != nil && *req.MemoryBytes < 1 {
		fields["memory_bytes"] = "Must be positive."
	}
	if req.DiskBytes != nil && *req.DiskBytes < 1 {
		fields["disk_bytes"] = "Must be positive."
	}
	in.CPUCores, in.MemoryBytes, in.DiskBytes = req.CPUCores, req.MemoryBytes, req.DiskBytes
	if len(req.Labels) > 20 {
		fields["labels"] = "At most 20 labels."
	}
	for k, v := range req.Labels {
		if !labelKeyRe.MatchString(k) || len(v) > 255 {
			fields["labels"] = "Label keys use letters, digits and underscores; values at most 255 characters."
		}
	}
	if req.GroupID != nil && *req.GroupID != "" {
		if !uuidRe.MatchString(*req.GroupID) {
			fields["group_id"] = "Unknown group."
		} else if ok, err := a.inv.GroupExists(ctx, *req.GroupID); err != nil {
			return in, nil, err
		} else if !ok {
			fields["group_id"] = "Unknown group."
		}
	}
	in.GroupID = req.GroupID

	var ep *inventory.Endpoint
	if req.ExporterURL != nil {
		e, err := a.guard.Check(ctx, *req.ExporterURL)
		if fe, ok := err.(*inventory.FieldError); ok {
			fields[fe.Field] = fe.Message
		} else if err != nil {
			return in, nil, err
		}
		ep = e
		in.Endpoint = e
		if create && (in.Hostname == nil || *in.Hostname == "") && e != nil {
			in.Hostname = &e.Host
		}
	} else if create {
		fields["exporter_url"] = "Required."
	}
	if len(fields) > 0 {
		return in, nil, errValidation(fields)
	}
	return in, ep, nil
}

func (a *API) validateServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExporterURL string `json:"exporter_url"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	ep, err := a.guard.Check(r.Context(), req.ExporterURL)
	if err != nil {
		fail(w, r, err)
		return
	}
	probe := a.guard.Probe(r.Context(), ep)
	writeJSON(w, http.StatusOK, map[string]any{
		"exporter_url": ep.CleanURL, "target_addr": ep.TargetAddr, "host": ep.Host, "validation": probe,
	})
}

// probeGate refuses unreachable endpoints unless an admin forces it.
func probeGate(r *http.Request, probe inventory.ProbeResult, force bool) error {
	if probe.Reachable && probe.NodeExporter {
		return nil
	}
	if force && auth.UserFrom(r.Context()).Role.AtLeast(auth.RoleAdmin) {
		return nil
	}
	msg := "The exporter endpoint did not answer like Node Exporter: " + probe.Error
	if force {
		msg += " Only admins can save an unreachable endpoint."
	} else {
		msg += " An admin can save it anyway with force; it will show as unknown until the first scrape."
	}
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "endpoint_unreachable", Message: msg,
		Fields: map[string]string{"exporter_url": probe.Error}}
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	var req serverRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	in, ep, err := a.checkServer(r.Context(), &req, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	probe := a.guard.Probe(r.Context(), ep)
	if err := probeGate(r, probe, req.Force); err != nil {
		fail(w, r, err)
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	s, err := a.inv.Create(r.Context(), in, enabled, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger()
	writeJSON(w, http.StatusCreated, map[string]any{"server": s, "status": map[bool]string{true: inventory.StatusUnknown, false: inventory.StatusDisabled}[enabled], "validation": probe})
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	var req serverRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Enabled != nil {
		fail(w, r, errValidation(map[string]string{"enabled": "Use the enable or disable action."}))
		return
	}
	in, ep, err := a.checkServer(r.Context(), &req, false)
	if err != nil {
		fail(w, r, err)
		return
	}
	var probe *inventory.ProbeResult
	if ep != nil {
		p := a.guard.Probe(r.Context(), ep)
		if err := probeGate(r, p, req.Force); err != nil {
			fail(w, r, err)
			return
		}
		probe = &p
	}
	s, err := a.inv.Update(r.Context(), id, in, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger()
	writeJSON(w, http.StatusOK, map[string]any{"server": s, "validation": probe})
}

func (a *API) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	s, err := a.inv.SetEnabled(r.Context(), id, enabled, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger()
	writeJSON(w, http.StatusOK, map[string]any{"server": s})
}

func (a *API) enableServer(w http.ResponseWriter, r *http.Request)  { a.setEnabled(w, r, true) }
func (a *API) disableServer(w http.ResponseWriter, r *http.Request) { a.setEnabled(w, r, false) }

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	var req struct {
		ConfirmName string `json:"confirm_name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	s, err := a.inv.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if req.ConfirmName != s.Name {
		fail(w, r, errValidation(map[string]string{"confirm_name": "Type the server name exactly to confirm."}))
		return
	}
	if err := a.inv.Delete(r.Context(), id, a.event(r)); err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger()
	w.WriteHeader(http.StatusNoContent)
}

// ---- detail and history ----

func (a *API) getServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	s, err := a.inv.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	snap, snapErr := a.snapshot(r.Context())
	v := &serverView{Server: s}
	if snapErr == nil && s.Enabled {
		v.Metrics = snap.Servers[s.ID]
	}
	v.Status = inventory.Status(s, v.Metrics, time.Now())

	pricing, err := a.costs.ServerPricingHistory(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	var serverCost *cost.ServerCost
	now := time.Now()
	y, m, _ := a.costs.ParsePeriod("", now)
	if res, err := a.costs.Month(r.Context(), y, m, now); err == nil {
		for i := range res.Servers {
			if res.Servers[i].ServerID == id {
				serverCost = &res.Servers[i]
				if serverCost.Priced {
					v.MonthlyCost = &serverCost.Projected
				}
			}
		}
	}
	open, _, err := a.alerts.List(r.Context(), alerts.Filter{ServerID: id, States: []string{"pending", "firing", "acknowledged"}, Page: 1, PageSize: 50})
	if err != nil {
		fail(w, r, err)
		return
	}
	recent, _, err := a.alerts.List(r.Context(), alerts.Filter{ServerID: id, States: []string{"resolved"}, Page: 1, PageSize: 10})
	if err != nil {
		fail(w, r, err)
		return
	}
	var asOf *time.Time
	if snap != nil {
		asOf = &snap.AsOf
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server": v, "as_of": asOf, "metrics_available": snapErr == nil,
		"pricing": pricing, "cost": serverCost, "currency": a.costs.Currency(),
		"open_alerts": open, "recent_incidents": recent,
	})
}

func (a *API) serverMetrics(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	if _, err := a.inv.Get(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	rng, ok := metrics.Ranges[r.URL.Query().Get("range")]
	if r.URL.Query().Get("range") == "" {
		rng, ok = metrics.Ranges["24h"], true
	}
	if !ok {
		fail(w, r, errValidation(map[string]string{"range": "Use one of 1h, 6h, 24h, 7d, 30d."}))
		return
	}
	series := splitCSV(r.URL.Query().Get("series"))
	if len(series) == 0 {
		series = metrics.AllSeries
	}
	for _, s := range series {
		if !contains(metrics.AllSeries, s) {
			fail(w, r, errValidation(map[string]string{"series": "Use cpu, memory, disk, network, load."}))
			return
		}
	}
	out, coverage, err := a.prom.History(r.Context(), id, rng, series)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": rng.Name, "step_s": int(rng.Step.Seconds()), "series": out, "coverage": coverage})
}

// ---- groups ----

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	g, err := a.inv.ListGroups(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": g})
}

type groupRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (a *API) saveGroup(w http.ResponseWriter, r *http.Request, id string) {
	var req groupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		fail(w, r, errValidation(map[string]string{"name": "Use 1–64 characters."}))
		return
	}
	g, err := a.inv.SaveGroup(r.Context(), id, req.Name, req.Description, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger() // the group name is a Prometheus label
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, g)
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request) { a.saveGroup(w, r, "") }
func (a *API) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	a.saveGroup(w, r, id)
}

func (a *API) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	if err := a.inv.DeleteGroup(r.Context(), id, a.event(r)); err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger()
	w.WriteHeader(http.StatusNoContent)
}

// ---- fleet summary ----

func (a *API) fleetSummary(w http.ResponseWriter, r *http.Request) {
	servers, err := a.inv.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	views, snap := a.views(r.Context(), servers)
	counts := map[string]int{"registered": len(views), "enabled": 0, "disabled": 0, "online": 0, "offline": 0, "unknown": 0}
	var cpuBusy, cpuCores, memUsed, memTotal, diskUsed, diskTotal, rx, tx float64
	var cpuN, memN, diskN, netN int
	for _, v := range views {
		counts[v.Status]++
		if v.Enabled {
			counts["enabled"]++
		}
		m := v.Metrics
		if m == nil || v.Status == inventory.StatusUnknown || v.Status == inventory.StatusDisabled {
			continue
		}
		if m.CPU != nil && m.Cores != nil {
			cpuBusy, cpuCores, cpuN = cpuBusy+*m.CPU**m.Cores, cpuCores+*m.Cores, cpuN+1
		}
		if m.MemUsedBytes != nil && m.MemTotalBytes != nil {
			memUsed, memTotal, memN = memUsed+*m.MemUsedBytes, memTotal+*m.MemTotalBytes, memN+1
		}
		if m.DiskUsedBytes != nil && m.DiskTotalBytes != nil {
			diskUsed, diskTotal, diskN = diskUsed+*m.DiskUsedBytes, diskTotal+*m.DiskTotalBytes, diskN+1
		}
		if m.NetRx != nil && m.NetTx != nil {
			rx, tx, netN = rx+*m.NetRx, tx+*m.NetTx, netN+1
		}
	}
	ratio := func(num, den float64, n int) map[string]any {
		out := map[string]any{"value": nil, "unit": "ratio", "servers_with_data": n}
		if n > 0 && den > 0 {
			out["value"] = num / den
		}
		return out
	}
	util := map[string]any{
		"cpu":    ratio(cpuBusy, cpuCores, cpuN),
		"memory": ratio(memUsed, memTotal, memN),
		"disk":   ratio(diskUsed, diskTotal, diskN),
		"network": map[string]any{"rx": nilIfNone(rx, netN), "tx": nilIfNone(tx, netN),
			"unit": "bytes_per_second", "servers_with_data": netN},
		"capacity": map[string]any{"cores": cpuCores, "memory_bytes": memTotal, "disk_bytes": diskTotal},
	}
	alertCounts, err := a.alerts.Counts(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	costOut := map[string]any{"status": "not_configured", "currency": a.costs.Currency()}
	if configured, err := a.costs.Configured(r.Context()); err == nil && configured {
		now := time.Now()
		y, m, _ := a.costs.ParsePeriod("", now)
		if res, err := a.costs.Month(r.Context(), y, m, now); err == nil {
			costOut = map[string]any{"status": "configured", "period": res.Period, "currency": a.costs.Currency(),
				"to_date": res.ToDate, "projected": res.Projected, "kind": "estimated",
				"servers_without_pricing": len(res.WithoutPricing)}
		}
	}
	var asOf *time.Time
	if snap != nil {
		asOf = &snap.AsOf
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"as_of": asOf, "monitoring": map[string]any{"available": snap != nil},
		"servers": counts, "utilization": util, "alerts": alertCounts, "cost": costOut,
	})
}

func nilIfNone(v float64, n int) any {
	if n == 0 {
		return nil
	}
	return v
}
