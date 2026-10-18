package httpapi

import (
	"context"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/cost"
)

func (a *API) listAlerts(w http.ResponseWriter, r *http.Request) {
	page, size, err := paging(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := alerts.Filter{States: splitCSV(q.Get("state")), Severities: splitCSV(q.Get("severity")), Page: page, PageSize: size}
	for _, s := range f.States {
		if !contains([]string{"pending", "firing", "acknowledged", "resolved"}, s) {
			fail(w, r, errValidation(map[string]string{"state": "Use pending, firing, acknowledged or resolved."}))
			return
		}
	}
	for _, s := range f.Severities {
		if !contains([]string{"info", "warning", "critical"}, s) {
			fail(w, r, errValidation(map[string]string{"severity": "Use info, warning or critical."}))
			return
		}
	}
	if s := q.Get("server"); s != "" {
		if !uuidRe.MatchString(s) {
			fail(w, r, errValidation(map[string]string{"server": "Unknown server."}))
			return
		}
		f.ServerID = s
	}
	items, total, err := a.alerts.List(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[alerts.Alert]{Items: items, Page: page, PageSize: size, Total: total})
}

func (a *API) ackAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	if err := a.alerts.Acknowledge(r.Context(), id, auth.UserFrom(r.Context()).ID, a.event(r)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- rules ----

func (a *API) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := a.alerts.ListRules(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rules})
}

type ruleRequest struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Threshold     float64 `json:"threshold"`
	ForSeconds    int     `json:"for_seconds"`
	Severity      string  `json:"severity"`
	ScopeGroupID  *string `json:"scope_group_id"`
	ScopeServerID *string `json:"scope_server_id"`
	Enabled       *bool   `json:"enabled"`
	RepeatAfterS  *int    `json:"repeat_after_s"`
}

var ruleKinds = []string{"unreachable", "cpu", "memory", "disk", "disk_forecast", "cost_increase"}

func (a *API) saveRule(w http.ResponseWriter, r *http.Request, id string) {
	var req ruleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	var existing *alerts.Rule
	if id != "" {
		e, err := a.alerts.GetRule(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		existing = e
		req.Kind = e.Kind // kind is fixed once created
	}
	f := map[string]string{}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 80 {
		f["name"] = "Use 1–80 characters."
	}
	if existing == nil && !contains(ruleKinds, req.Kind) {
		f["kind"] = "Use unreachable, cpu, memory, disk, disk_forecast or cost_increase."
	}
	switch req.Kind {
	case "cpu", "memory", "disk":
		if req.Threshold <= 0 || req.Threshold > 100 {
			f["threshold"] = "A percentage between 1 and 100."
		}
	case "disk_forecast":
		if req.Threshold < 1 || req.Threshold > 720 {
			f["threshold"] = "Hours between 1 and 720."
		}
	case "cost_increase":
		if req.Threshold <= 0 || req.Threshold > 1000 {
			f["threshold"] = "A percentage increase between 1 and 1000."
		}
	}
	if req.ForSeconds < 0 || req.ForSeconds > 86400 {
		f["for_seconds"] = "Between 0 and 86400."
	}
	if !contains([]string{"info", "warning", "critical"}, req.Severity) {
		f["severity"] = "Use info, warning or critical."
	}
	for _, p := range []**string{&req.ScopeGroupID, &req.ScopeServerID} {
		if *p != nil && **p == "" {
			*p = nil
		}
		if *p != nil && !uuidRe.MatchString(**p) {
			f["scope"] = "Unknown server or group."
		}
	}
	if req.ScopeGroupID != nil && req.ScopeServerID != nil {
		f["scope"] = "Choose a server or a group, not both."
	}
	if (req.Kind == "cost_increase" || req.Kind == "monitoring") && (req.ScopeGroupID != nil || req.ScopeServerID != nil) {
		f["scope"] = "This rule applies to the whole fleet."
	}
	repeat := 14400
	if req.RepeatAfterS != nil {
		if *req.RepeatAfterS < 0 || *req.RepeatAfterS > 7*86400 {
			f["repeat_after_s"] = "Between 0 (never) and 604800."
		}
		repeat = *req.RepeatAfterS
	} else if existing != nil {
		repeat = existing.RepeatAfterS
	}
	if len(f) > 0 {
		fail(w, r, errValidation(f))
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	rule := alerts.Rule{Name: req.Name, Kind: req.Kind, Threshold: req.Threshold, ForSeconds: req.ForSeconds, Severity: req.Severity,
		ScopeGroupID: req.ScopeGroupID, ScopeServerID: req.ScopeServerID, Enabled: enabled, RepeatAfterS: repeat}
	out, err := a.alerts.SaveRule(r.Context(), id, rule, a.event(r))
	if err != nil {
		if err.Error() == "scope server or group not found" {
			fail(w, r, errValidation(map[string]string{"scope": "Unknown server or group."}))
			return
		}
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (a *API) createRule(w http.ResponseWriter, r *http.Request) { a.saveRule(w, r, "") }
func (a *API) updateRule(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	a.saveRule(w, r, id)
}

func (a *API) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	if err := a.alerts.DeleteRule(r.Context(), id, a.event(r)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- notification channels ----

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) {
	c, err := a.alerts.ListChannels(r.Context(), a.senders)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": c, "available": map[string]bool{
		"email": a.senders.Available("email"), "slack": a.senders.Available("slack"),
		"telegram": a.senders.Available("telegram"), "discord": a.senders.Available("discord"),
	}})
}

type channelRequest struct {
	Kind        string               `json:"kind"`
	Name        string               `json:"name"`
	Config      alerts.ChannelConfig `json:"config"`
	MinSeverity string               `json:"min_severity"`
	Enabled     *bool                `json:"enabled"`
	GroupIDs    []string             `json:"group_ids"` // empty = every alert
}

func (a *API) saveChannel(w http.ResponseWriter, r *http.Request, id string) {
	var req channelRequest
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if id != "" {
		existing, err := a.alerts.GetChannel(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		req.Kind = existing.Kind
	}
	f := map[string]string{}
	if !contains([]string{"email", "slack", "telegram", "discord"}, req.Kind) {
		f["kind"] = "Use email, slack, telegram or discord."
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 80 {
		f["name"] = "Use 1–80 characters."
	}
	if req.MinSeverity == "" {
		req.MinSeverity = "warning"
	}
	if !contains([]string{"info", "warning", "critical"}, req.MinSeverity) {
		f["min_severity"] = "Use info, warning or critical."
	}
	if req.Kind == "email" {
		if len(req.Config.To) == 0 || len(req.Config.To) > 20 {
			f["config.to"] = "Add 1–20 recipient addresses."
		}
		for i, addr := range req.Config.To {
			req.Config.To[i] = strings.TrimSpace(addr)
			if _, err := mail.ParseAddress(req.Config.To[i]); err != nil {
				f["config.to"] = "Invalid address: " + addr
			}
		}
	} else {
		req.Config.To = nil
	}
	if req.Kind != "telegram" {
		req.Config.ChatID = ""
	}
	groupIDs, msg, err := a.checkGroupIDs(r.Context(), req.GroupIDs)
	if err != nil {
		fail(w, r, err)
		return
	}
	if msg != "" {
		f["group_ids"] = msg
	}
	if len(f) > 0 {
		fail(w, r, errValidation(f))
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	c, err := a.alerts.SaveChannel(r.Context(), id, alerts.Channel{Kind: req.Kind, Name: req.Name, Config: req.Config,
		MinSeverity: req.MinSeverity, Enabled: enabled, GroupIDs: groupIDs}, a.event(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	c.Configured = a.senders.Available(c.Kind)
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, c)
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) { a.saveChannel(w, r, "") }
func (a *API) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	a.saveChannel(w, r, id)
}

// testChannel sends one test message right away and reports the outcome.
func (a *API) testChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	c, err := a.alerts.GetChannel(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	u := auth.UserFrom(r.Context())
	err = a.senders.Send(ctx, c.Kind, c.Config, alerts.Message{
		Subject: "[TEST] BM Monitor notification channel \"" + c.Name + "\"",
		Body: "This is a test message from BM Monitor, sent by " + u.Email + " at " + time.Now().UTC().Format("2006-01-02 15:04:05") +
			" UTC.\n\nIf you received it, alerts for this channel will arrive here.\n\n-- BM Monitor",
	})
	ev := a.event(r)
	ev.Action, ev.TargetType, ev.TargetID = "channel.test", "notification_channel", id
	result := map[string]any{"sent": err == nil}
	if err != nil {
		result["error"] = err.Error()
	}
	ev.After = result
	_ = recordAudit(r.Context(), a, ev)
	writeJSON(w, http.StatusOK, result)
}

// ---- insights ----

func (a *API) underutilized(w http.ResponseWriter, r *http.Request) {
	rep, err := a.insights.Underutilized(r.Context(), time.Now().UTC())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *API) compare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := q.Get("metric")
	if metric == "" {
		metric = "cpu"
	}
	if !contains([]string{"cpu", "memory", "disk"}, metric) {
		fail(w, r, errValidation(map[string]string{"metric": "Use cpu, memory or disk."}))
		return
	}
	now := time.Now()
	cur := now.In(a.cfg.BillingTimezone)
	def := func(key string, back int) (time.Time, error) {
		v := q.Get(key)
		if v == "" {
			return time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, a.cfg.BillingTimezone).AddDate(0, -back, 0), nil
		}
		t, err := time.ParseInLocation("2006-01", v, a.cfg.BillingTimezone)
		if err != nil {
			return t, &cost.ValidationError{Field: key, Message: "Use YYYY-MM."}
		}
		return t, nil
	}
	ma, err := def("a", 1)
	if err != nil {
		fail(w, r, err)
		return
	}
	mb, err := def("b", 0)
	if err != nil {
		fail(w, r, err)
		return
	}
	group := q.Get("group")
	if group != "" && !uuidRe.MatchString(group) {
		fail(w, r, errValidation(map[string]string{"group": "Unknown group."}))
		return
	}
	rows, err := a.insights.Compare(r.Context(), metric, ma, mb, group, now)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metric": metric, "unit": "ratio", "a": ma.Format("2006-01"), "b": mb.Format("2006-01"),
		"note": "Average utilisation over each month (the current month up to now).", "items": rows})
}
