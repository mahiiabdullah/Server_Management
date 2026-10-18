package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/auth"
)

// People are dashboard users and alert-only contacts. Admins tag them with
// groups; each person then gets email alerts for the servers in those groups.

var severities = []string{"info", "warning", "critical"}

// personView is an account with its alert groups.
type personView struct {
	*auth.Account
	AlertGroups []alerts.PersonGroup `json:"alert_groups"`
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.auth.ListAccounts(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	groups, err := a.alerts.GroupsOfPeople(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]personView, 0, len(users))
	for i := range users {
		g := groups[users[i].ID]
		if g == nil {
			g = []alerts.PersonGroup{}
		}
		items = append(items, personView{Account: &users[i], AlertGroups: g})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "sso_enabled": a.cfg.GoogleSSO(), "email_configured": a.cfg.SMTP.Enabled()})
}

func (a *API) personResponse(ctx context.Context, acc *auth.Account) (*personView, error) {
	g, err := a.alerts.PersonGroups(ctx, acc.ID)
	if err != nil {
		return nil, err
	}
	return &personView{Account: acc, AlertGroups: g}, nil
}

// checkAlertGroups validates an alert group list into f.
func (a *API) checkAlertGroups(ctx context.Context, ids *[]string, f map[string]string) ([]string, error) {
	if ids == nil {
		return nil, nil
	}
	out, msg, err := a.checkGroupIDs(ctx, *ids)
	if err != nil {
		return nil, err
	}
	if msg != "" {
		f["alert_group_ids"] = msg
	}
	return out, nil
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind             string    `json:"kind"` // user (default) | contact
		Email            string    `json:"email"`
		Name             string    `json:"name"`
		Role             string    `json:"role"`
		Password         string    `json:"password"`
		AlertsAll        bool      `json:"alerts_all"`
		AlertMinSeverity string    `json:"alert_min_severity"`
		AlertGroupIDs    *[]string `json:"alert_group_ids"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	f := map[string]string{}
	req.Email, req.Name = strings.TrimSpace(req.Email), strings.TrimSpace(req.Name)
	if req.Kind == "" {
		req.Kind = auth.KindUser
	}
	if _, err := mail.ParseAddress(req.Email); err != nil || len(req.Email) > 254 {
		f["email"] = "Enter a valid email address."
	}
	if req.Name == "" || len(req.Name) > 80 {
		f["name"] = "Use 1–80 characters."
	}
	if req.AlertMinSeverity == "" {
		req.AlertMinSeverity = "warning"
	}
	if !contains(severities, req.AlertMinSeverity) {
		f["alert_min_severity"] = "Use info, warning or critical."
	}
	switch req.Kind {
	case auth.KindUser:
		if !auth.Role(req.Role).Valid() {
			f["role"] = "Use admin, operator or viewer."
		}
		if req.Password == "" && !a.cfg.GoogleSSO() {
			f["password"] = "Required (Google sign-in is not configured)."
		} else if req.Password != "" && len(req.Password) < 12 {
			f["password"] = "Use at least 12 characters."
		}
	case auth.KindContact:
		if req.Password != "" {
			f["password"] = "Alert-only contacts cannot sign in, so they have no password."
		}
	default:
		f["kind"] = "Use user or contact."
	}
	groupIDs, err := a.checkAlertGroups(r.Context(), req.AlertGroupIDs, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(f) > 0 {
		fail(w, r, errValidation(f))
		return
	}
	prefs := auth.AlertPrefs{All: req.AlertsAll, MinSeverity: req.AlertMinSeverity}
	var acc *auth.Account
	if req.Kind == auth.KindContact {
		acc, err = a.auth.CreateContact(r.Context(), req.Email, req.Name, prefs)
	} else {
		acc, err = a.auth.CreateAccount(r.Context(), req.Email, req.Name, auth.Role(req.Role), req.Password, prefs)
	}
	if errors.Is(err, auth.ErrEmailTaken) {
		fail(w, r, &APIError{Status: http.StatusConflict, Code: "conflict", Message: err.Error(), Fields: map[string]string{"email": err.Error()}})
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	ev := a.event(r)
	ev.Action, ev.TargetType, ev.TargetID, ev.After = "user.create", "user", acc.ID, acc
	if acc.Kind == auth.KindContact {
		ev.Action = "contact.create"
	}
	_ = recordAudit(r.Context(), a, ev)
	if groupIDs != nil {
		if err := a.alerts.SetPersonGroups(r.Context(), acc.ID, groupIDs, a.event(r)); err != nil {
			fail(w, r, err)
			return
		}
	}
	out, err := a.personResponse(r.Context(), acc)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	var req struct {
		Name             *string   `json:"name"`
		Role             *string   `json:"role"`
		Disabled         *bool     `json:"disabled"`
		Password         *string   `json:"password"`
		Unlock           bool      `json:"unlock"`
		AlertsAll        *bool     `json:"alerts_all"`
		AlertMinSeverity *string   `json:"alert_min_severity"`
		AlertGroupIDs    *[]string `json:"alert_group_ids"`
		ResumeAlerts     bool      `json:"resume_alerts"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	before, err := a.auth.GetAccount(r.Context(), id)
	if err != nil {
		fail(w, r, errNotFound)
		return
	}
	f := map[string]string{}
	p := auth.AccountPatch{Name: req.Name, Disabled: req.Disabled, Password: req.Password, Unlock: req.Unlock,
		AlertsAll: req.AlertsAll, AlertMinSeverity: req.AlertMinSeverity, ResumeAlerts: req.ResumeAlerts}
	if req.Name != nil && (strings.TrimSpace(*req.Name) == "" || len(*req.Name) > 80) {
		f["name"] = "Use 1–80 characters."
	}
	if req.Role != nil {
		role := auth.Role(*req.Role)
		if !role.Valid() {
			f["role"] = "Use admin, operator or viewer."
		} else if before.Kind == auth.KindContact && role != auth.RoleViewer {
			f["role"] = "Alert-only contacts cannot sign in, so they have no role."
		}
		p.Role = &role
	}
	if req.Password != nil {
		if before.Kind == auth.KindContact {
			f["password"] = "Alert-only contacts cannot sign in, so they have no password."
		} else if len(*req.Password) < 12 {
			f["password"] = "Use at least 12 characters."
		}
	}
	if req.AlertMinSeverity != nil && !contains(severities, *req.AlertMinSeverity) {
		f["alert_min_severity"] = "Use info, warning or critical."
	}
	me := auth.UserFrom(r.Context())
	if id == me.ID && ((req.Disabled != nil && *req.Disabled) || (req.Role != nil && *req.Role != string(auth.RoleAdmin))) {
		f["role"] = "You cannot disable or demote yourself."
	}
	groupIDs, err := a.checkAlertGroups(r.Context(), req.AlertGroupIDs, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(f) > 0 {
		fail(w, r, errValidation(f))
		return
	}
	acc, err := a.auth.UpdateAccount(r.Context(), id, p)
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		fail(w, r, &APIError{Status: http.StatusConflict, Code: "last_admin", Message: err.Error()})
		return
	case errors.Is(err, auth.ErrNoSession):
		fail(w, r, errNotFound)
		return
	case err != nil:
		fail(w, r, err)
		return
	}
	ev := a.event(r)
	ev.Action, ev.TargetType, ev.TargetID, ev.Before, ev.After = "user.update", "user", id, before, acc
	if req.Password != nil {
		ev.Action = "user.password_reset"
	}
	_ = recordAudit(r.Context(), a, ev)
	if groupIDs != nil {
		if err := a.alerts.SetPersonGroups(r.Context(), id, groupIDs, a.event(r)); err != nil {
			fail(w, r, err)
			return
		}
	}
	out, err := a.personResponse(r.Context(), acc)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- a person's own alert settings ----

func (a *API) myAlerts(w http.ResponseWriter, r *http.Request) {
	p, err := a.alerts.OwnPrefs(r.Context(), auth.UserFrom(r.Context()).ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

const maxPause = 90 * 24 * time.Hour

func (a *API) updateMyAlerts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MinSeverity *string         `json:"min_severity"`
		Pause       *string         `json:"pause"` // 1d | 7d | resume | an RFC 3339 time
		Muted       map[string]bool `json:"muted"` // group id → muted
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	f := map[string]string{}
	p := alerts.PrefsPatch{MinSeverity: req.MinSeverity}
	if req.MinSeverity != nil && !contains(severities, *req.MinSeverity) {
		f["min_severity"] = "Use info, warning or critical."
	}
	if req.Pause != nil {
		now := time.Now()
		var until time.Time
		switch *req.Pause {
		case "1d":
			until = alerts.PauseUntil(alerts.LinkPause1d, now)
		case "7d":
			until = alerts.PauseUntil(alerts.LinkPause7d, now)
		case "resume":
		default:
			t, err := time.Parse(time.RFC3339, *req.Pause)
			if err != nil || !t.After(now) || t.Sub(now) > maxPause {
				f["pause"] = "Use 1d, 7d, resume, or a time within the next 90 days."
			}
			until = t
		}
		p.Pause = &until
	}
	if len(req.Muted) > 0 {
		p.Muted = map[string]bool{}
		for gid, m := range req.Muted {
			gid = strings.ToLower(gid)
			if !uuidRe.MatchString(gid) {
				f["muted"] = "Unknown group."
			}
			p.Muted[gid] = m
		}
	}
	if len(f) > 0 {
		fail(w, r, errValidation(f))
		return
	}
	out, err := a.alerts.UpdateOwnPrefs(r.Context(), auth.UserFrom(r.Context()).ID, p, a.event(r))
	if errors.Is(err, alerts.ErrUnknownGroup) {
		fail(w, r, errValidation(map[string]string{"muted": "You can only mute groups an admin added you to."}))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- pause links in emails (no sign-in: the signed link is the credential) ----

func (a *API) checkAlertLink(r *http.Request, token string) (*auth.Account, string, error) {
	uid, action, err := a.links.Verify(token, time.Now())
	if err != nil {
		return nil, "", err
	}
	acc, err := a.auth.GetAccount(r.Context(), uid)
	if err != nil || acc.Disabled {
		return nil, "", alerts.ErrBadLink
	}
	return acc, action, nil
}

func linkError(w http.ResponseWriter, r *http.Request) {
	fail(w, r, &APIError{Status: http.StatusBadRequest, Code: "bad_link", Message: alerts.ErrBadLink.Error()})
}

func (a *API) alertLinkInfo(w http.ResponseWriter, r *http.Request) {
	acc, action, err := a.checkAlertLink(r, r.URL.Query().Get("t"))
	if err != nil {
		linkError(w, r)
		return
	}
	var paused *time.Time
	if acc.AlertsPausedUntil != nil && acc.AlertsPausedUntil.After(time.Now()) {
		paused = acc.AlertsPausedUntil
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": acc.Name, "action": action, "paused_until": paused})
}

func (a *API) applyAlertLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"t"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	acc, action, err := a.checkAlertLink(r, req.Token)
	if err != nil {
		linkError(w, r)
		return
	}
	until := alerts.PauseUntil(action, time.Now())
	ev := a.event(r)
	ev.ActorID = acc.ID // the person acts on themselves through the link
	out, err := a.alerts.UpdateOwnPrefs(r.Context(), acc.ID, alerts.PrefsPatch{Pause: &until}, ev)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": acc.Name, "action": action, "paused_until": out.PausedUntil})
}

// ---- group people ----

func (a *API) groupPeople(ctx context.Context, groupID string, withEmail bool) ([]alerts.GroupPerson, error) {
	people, err := a.alerts.GroupPeople(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if !withEmail {
		for i := range people {
			people[i].Email = ""
		}
	}
	return people, nil
}

func (a *API) setGroupPeople(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	ids, msg := checkIDs(req.UserIDs, 1000, "person")
	if msg != "" {
		fail(w, r, errValidation(map[string]string{"user_ids": msg}))
		return
	}
	err := a.alerts.SetGroupPeople(r.Context(), id, ids, a.event(r))
	if errors.Is(err, alerts.ErrUnknownPerson) {
		fail(w, r, errValidation(map[string]string{"user_ids": "One or more people do not exist."}))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	people, err := a.groupPeople(r.Context(), id, true)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": people})
}

// ---- delivery log ----

func (a *API) alertDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r, "id")
	if !ok {
		fail(w, r, errNotFound)
		return
	}
	d, err := a.alerts.Deliveries(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": d})
}
