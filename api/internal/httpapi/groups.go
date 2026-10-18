package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/inventory"
)

// A group is a product or purpose (e.g. puku-web-chat). A server can be in any
// number of groups, and a group can have any number of servers.

const maxGroupsPerServer = 50

// checkIDs lowercases, de-duplicates and validates a list of UUIDs.
func checkIDs(ids []string, limit int, what string) ([]string, string) {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidRe.MatchString(id) {
			return nil, "Unknown " + what + "."
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > limit {
		return nil, "Too many entries."
	}
	return out, ""
}

// checkGroupIDs validates a server's group list; msg is a field error.
func (a *API) checkGroupIDs(ctx context.Context, ids []string) ([]string, string, error) {
	out, msg := checkIDs(ids, maxGroupsPerServer, "group")
	if msg != "" || len(out) == 0 {
		return out, msg, nil
	}
	ok, err := a.inv.GroupsExist(ctx, out)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "Unknown group.", nil
	}
	return out, "", nil
}

// groupSummary is the live state of one group, for the groups overview.
type groupSummary struct {
	inventory.Group
	Status     map[string]int   `json:"status"`      // online | offline | unknown | disabled → servers
	OpenAlerts map[string]int   `json:"open_alerts"` // severity → firing or acknowledged alerts
	ToDate     *decimal.Decimal `json:"cost_to_date"`
	Projected  *decimal.Decimal `json:"cost_projected"`
	Shared     int              `json:"shared_servers"` // members that are also in another group
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.inv.ListGroups(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if r.URL.Query().Get("summary") != "1" {
		writeJSON(w, http.StatusOK, map[string]any{"items": groups})
		return
	}
	ctx := r.Context()
	servers, err := a.inv.List(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	views, snap := a.views(ctx, servers)
	open, err := a.alerts.OpenByServer(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	costs := a.groupCosts(ctx)
	items := make([]groupSummary, 0, len(groups))
	for _, g := range groups {
		s := groupSummary{Group: g, Status: map[string]int{inventory.StatusOnline: 0, inventory.StatusOffline: 0,
			inventory.StatusUnknown: 0, inventory.StatusDisabled: 0}, OpenAlerts: map[string]int{"critical": 0, "warning": 0, "info": 0}}
		for _, v := range views {
			if !v.InGroup(g.ID) {
				continue
			}
			s.Status[v.Status]++
			for sev, n := range open[v.ID] {
				s.OpenAlerts[sev] += n
			}
			if len(v.Groups) > 1 {
				s.Shared++
			}
		}
		if gc, ok := costs[g.ID]; ok {
			s.ToDate, s.Projected = &gc.ToDate, &gc.Projected
		}
		items = append(items, s)
	}
	var asOf *time.Time
	if snap != nil {
		asOf = &snap.AsOf
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "as_of": asOf, "metrics_available": snap != nil,
		"currency": a.costs.Currency(), "costs_configured": costs != nil})
}

// groupCosts returns this month's cost per group, or nil when costs are not configured.
func (a *API) groupCosts(ctx context.Context) map[string]cost.GroupCost {
	if ok, err := a.costs.Configured(ctx); err != nil || !ok {
		return nil
	}
	now := time.Now()
	y, m, _ := a.costs.ParsePeriod("", now)
	res, err := a.costs.Month(ctx, y, m, now)
	if err != nil {
		loggerFrom(ctx).Warn("group costs failed", "err", err)
		return nil
	}
	out := map[string]cost.GroupCost{}
	for _, g := range res.Groups {
		out[g.GroupID] = g
	}
	return out
}

func (a *API) getGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	g, err := a.inv.GetGroup(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	all, err := a.inv.List(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	var members []*inventory.Server
	ids := []string{}
	for _, s := range all {
		if s.InGroup(id) {
			members = append(members, s)
			ids = append(ids, s.ID)
		}
	}
	views, snap := a.views(ctx, members)
	open, _, err := a.alerts.List(ctx, alerts.Filter{ServerIDs: ids, States: []string{"pending", "firing", "acknowledged"}, Page: 1, PageSize: 100})
	if err != nil {
		fail(w, r, err)
		return
	}
	var gc *cost.GroupCost
	costs := a.groupCosts(ctx)
	if c, ok := costs[id]; ok {
		gc = &c
	}
	// Everyone sees who gets the group's alerts; only admins see their addresses.
	people, err := a.groupPeople(ctx, id, auth.UserFrom(ctx).Role.AtLeast(auth.RoleAdmin))
	if err != nil {
		fail(w, r, err)
		return
	}
	var asOf *time.Time
	if snap != nil {
		asOf = &snap.AsOf
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group": g, "servers": views, "as_of": asOf, "metrics_available": snap != nil,
		"open_alerts": open, "cost": gc, "currency": a.costs.Currency(), "costs_configured": costs != nil,
		"people": people,
	})
}

type groupRequest struct {
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	ServerIDs   *[]string `json:"server_ids"` // create only: initial members
}

func (a *API) saveGroup(w http.ResponseWriter, r *http.Request, id string) {
	var req groupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	fields := map[string]string{}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 || strings.Contains(req.Name, ",") {
		fields["name"] = "Use 1–64 characters, no commas."
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if len(d) > 500 {
			fields["description"] = "At most 500 characters."
		}
		req.Description = &d
	}
	var serverIDs []string
	if req.ServerIDs != nil {
		if id != "" {
			fields["server_ids"] = "Change members with PUT /groups/{id}/servers."
		} else if ids, msg := checkIDs(*req.ServerIDs, 1000, "server"); msg != "" {
			fields["server_ids"] = msg
		} else {
			serverIDs = ids
		}
	}
	if len(fields) > 0 {
		fail(w, r, errValidation(fields))
		return
	}
	g, err := a.inv.SaveGroup(r.Context(), id, req.Name, req.Description, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(serverIDs) > 0 {
		if g, err = a.inv.SetGroupServers(r.Context(), g.ID, serverIDs, a.event(r)); err != nil {
			fail(w, r, err)
			return
		}
	}
	a.targets.Trigger() // group names are a Prometheus label
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

// setGroupServers replaces a group's members with the given servers.
func (a *API) setGroupServers(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	var req struct {
		ServerIDs []string `json:"server_ids"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	ids, msg := checkIDs(req.ServerIDs, 1000, "server")
	if msg != "" {
		fail(w, r, errValidation(map[string]string{"server_ids": msg}))
		return
	}
	g, err := a.inv.SetGroupServers(r.Context(), id, ids, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.targets.Trigger()
	writeJSON(w, http.StatusOK, g)
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
