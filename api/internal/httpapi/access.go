package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"sort"

	"github.com/go-chi/chi/v5"

	"bm-monitor/api/internal/access"
	"bm-monitor/api/internal/accessreq"
	"bm-monitor/api/internal/auth"
)

var grantIDRe = regexp.MustCompile(`^[0-9]{4,9}$`)

func (a *API) accessError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *accessreq.ValidationError
	var ce *accessreq.ConflictError
	switch {
	case errors.As(err, &ve):
		fail(w, r, errValidation(ve.Fields))
	case errors.As(err, &ce):
		fail(w, r, &APIError{Status: http.StatusConflict, Code: "conflict", Message: ce.Message})
	case errors.Is(err, accessreq.ErrNotConfigured):
		fail(w, r, &APIError{Status: http.StatusServiceUnavailable, Code: "access_not_configured", Message: err.Error()})
	case errors.Is(err, accessreq.ErrNotFound):
		fail(w, r, errNotFound)
	default:
		loggerFrom(r.Context()).Error("access workflow", "err", err)
		fail(w, r, &APIError{Status: http.StatusBadGateway, Code: "github_error",
			Message: "GitHub could not be reached or refused the change. Try again in a minute; the error has been logged."})
	}
}

// accessConfig tells the dashboard whether requests are possible, and on what terms.
func (a *API) accessConfig(w http.ResponseWriter, r *http.Request) {
	if !a.access.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false,
			"reason": "The SSH access workflow is not configured on this BM Monitor (GITHUB_REPO)."})
		return
	}
	p, err := a.access.Policy(r.Context())
	if err != nil {
		a.accessError(w, r, err)
		return
	}
	type srv struct {
		Name       string `json:"name"`
		Host       string `json:"host"`
		Production bool   `json:"production"`
	}
	servers := []srv{}
	for name, s := range p.Servers {
		servers = append(servers, srv{name, s.Host, s.Production})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	u := auth.UserFrom(r.Context())
	unix, uerr := access.UsernameFromEmail(u.Email)
	out := map[string]any{"enabled": true, "repo": a.access.Repo(), "servers": servers,
		"max_days": p.MaxDays, "max_sudo_days": p.MaxSudoDays, "admins": len(p.Admins), "unix_user": unix}
	if uerr != nil {
		out["unix_user_error"] = uerr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listAccessRequests(w http.ResponseWriter, r *http.Request) {
	if !a.access.Enabled() {
		a.accessError(w, r, accessreq.ErrNotConfigured)
		return
	}
	u := auth.UserFrom(r.Context())
	all := r.URL.Query().Get("all") == "1"
	if all && !u.Role.AtLeast(auth.RoleAdmin) {
		fail(w, r, errForbidden)
		return
	}
	items, err := a.access.List(r.Context(), u.Email, all)
	if err != nil {
		a.accessError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createAccessRequest(w http.ResponseWriter, r *http.Request) {
	if !a.access.Enabled() {
		a.accessError(w, r, accessreq.ErrNotConfigured)
		return
	}
	var req struct {
		Servers   []string `json:"servers"`
		Sudo      bool     `json:"sudo"`
		Days      int      `json:"days"`
		Reason    string   `json:"reason"`
		PublicKey string   `json:"public_key"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	u := auth.UserFrom(r.Context())
	res, err := a.access.Create(r.Context(), u.ID, u.Email, accessreq.Input{
		Servers: req.Servers, Sudo: req.Sudo, Days: req.Days, Reason: req.Reason, PublicKey: req.PublicKey,
	}, a.event(r))
	if err != nil {
		a.accessError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (a *API) listGrants(w http.ResponseWriter, r *http.Request) {
	if !a.access.Enabled() {
		a.accessError(w, r, accessreq.ErrNotConfigured)
		return
	}
	grants, st, err := a.access.Grants(r.Context())
	if err != nil {
		a.accessError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": grants, "applied": st})
}

func (a *API) revokeGrant(w http.ResponseWriter, r *http.Request) {
	if !a.access.Enabled() {
		a.accessError(w, r, accessreq.ErrNotConfigured)
		return
	}
	id := chi.URLParam(r, "grantID")
	if !grantIDRe.MatchString(id) {
		fail(w, r, errNotFound)
		return
	}
	res, err := a.access.Revoke(r.Context(), auth.UserFrom(r.Context()).ID, id, a.event(r))
	if err != nil {
		a.accessError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
