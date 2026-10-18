package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/auth"
)

func recordAudit(ctx context.Context, a *API, ev audit.Event) error {
	err := audit.Record(ctx, a.pool, ev)
	if err != nil {
		loggerFrom(ctx).Error("audit write failed", "err", err)
	}
	return err
}

// ---- own password ----

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if len(req.New) < 12 || len(req.New) > 256 {
		fail(w, r, errValidation(map[string]string{"new_password": "Use at least 12 characters."}))
		return
	}
	u := auth.UserFrom(r.Context())
	if err := a.auth.ChangePassword(r.Context(), u, req.Current, req.New); err != nil {
		if errors.Is(err, auth.ErrWrongPassword) {
			fail(w, r, errValidation(map[string]string{"current_password": "Current password is incorrect."}))
			return
		}
		fail(w, r, err)
		return
	}
	ev := a.event(r)
	ev.Action, ev.TargetType, ev.TargetID = "user.password_change", "user", u.ID
	_ = recordAudit(r.Context(), a, ev)
	w.WriteHeader(http.StatusNoContent)
}

// ---- settings ----

// editableSettings lists keys admins may change, with their validators.
var editableSettings = map[string]func(json.RawMessage) bool{
	"proration": func(v json.RawMessage) bool {
		var x struct {
			Enabled *bool `json:"enabled"`
		}
		return json.Unmarshal(v, &x) == nil && x.Enabled != nil
	},
	"underutil_window_days":  numberIn(1, 90),
	"underutil_cpu_p95":      numberIn(1, 100),
	"underutil_mem_p95":      numberIn(1, 100),
	"underutil_min_coverage": numberIn(0, 100),
}

func numberIn(lo, hi float64) func(json.RawMessage) bool {
	return func(v json.RawMessage) bool {
		var f float64
		return json.Unmarshal(v, &f) == nil && f >= lo && f <= hi
	}
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := a.pool.Query(r.Context(), `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		fail(w, r, err)
		return
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v json.RawMessage
		if err := rows.Scan(&k, &v); err != nil {
			fail(w, r, err)
			return
		}
		out[k] = v
	}
	editable := make([]string, 0, len(editableSettings))
	for k := range editableSettings {
		editable = append(editable, k)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"values": out, "editable": editable,
		"environment": map[string]any{
			"billing_timezone": a.cfg.BillingTimezone.String(), "billing_currency": a.cfg.BillingCurrency,
			"default_scrape_interval_s": int(a.cfg.DefaultScrapeInterval.Seconds()),
			"allowed_target_cidrs":      fmt.Sprint(a.cfg.AllowedTargetCIDRs), "allowed_target_ports": a.cfg.AllowedTargetPorts,
			"google_sso": a.cfg.GoogleSSO(), "email_configured": a.cfg.SMTP.Enabled(),
		},
	})
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, r, err)
		return
	}
	f := map[string]string{}
	for k, v := range req {
		valid, ok := editableSettings[k]
		switch {
		case !ok:
			f[k] = "This setting cannot be changed here."
		case !valid(v):
			f[k] = "Invalid value."
		}
	}
	if len(f) > 0 || len(req) == 0 {
		if len(req) == 0 {
			f["body"] = "Send at least one setting."
		}
		fail(w, r, errValidation(f))
		return
	}
	u := auth.UserFrom(r.Context())
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	for k, v := range req {
		var before json.RawMessage
		_ = tx.QueryRow(r.Context(), `SELECT value FROM settings WHERE key = $1`, k).Scan(&before)
		if _, err := tx.Exec(r.Context(), `INSERT INTO settings (key, value, updated_at, updated_by) VALUES ($1, $2, now(), $3)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by`,
			k, string(v), u.ID); err != nil {
			fail(w, r, err)
			return
		}
		ev := a.event(r)
		ev.Action, ev.TargetType, ev.TargetID, ev.Before, ev.After = "settings.change", "setting", k, before, v
		if err := audit.Record(r.Context(), tx, ev); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	a.getSettings(w, r)
}

// ---- audit log ----

type auditRow struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorID    *string         `json:"actor_id"`
	ActorEmail *string         `json:"actor_email"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   *string         `json:"target_id"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	IP         *string         `json:"ip"`
	RequestID  *string         `json:"request_id"`
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	page, size, err := paging(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	where, args := []string{"true"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if v := q.Get("action"); v != "" {
		add("e.action LIKE ?", strings.ReplaceAll(v, "%", "")+"%")
	}
	if v := q.Get("target_type"); v != "" {
		add("e.target_type = ?", v)
	}
	if v := q.Get("target_id"); v != "" {
		add("e.target_id = ?", v)
	}
	if v := q.Get("actor"); v != "" {
		add("lower(u.email) = lower(?)", v)
	}
	wh := strings.Join(where, " AND ")
	var total int
	if err := a.pool.QueryRow(r.Context(), `SELECT count(*) FROM audit_events e LEFT JOIN users u ON u.id = e.actor_id WHERE `+wh, args...).Scan(&total); err != nil {
		fail(w, r, err)
		return
	}
	args = append(args, size, (page-1)*size)
	rows, err := a.pool.Query(r.Context(), fmt.Sprintf(`
		SELECT e.id, e.at, e.actor_id, u.email, e.action, e.target_type, e.target_id, e.before, e.after, host(e.ip), e.request_id
		FROM audit_events e LEFT JOIN users u ON u.id = e.actor_id WHERE %s ORDER BY e.at DESC, e.id DESC LIMIT $%d OFFSET $%d`,
		wh, len(args)-1, len(args)), args...)
	if err != nil {
		fail(w, r, err)
		return
	}
	defer rows.Close()
	items := []auditRow{}
	for rows.Next() {
		var x auditRow
		if err := rows.Scan(&x.ID, &x.At, &x.ActorID, &x.ActorEmail, &x.Action, &x.TargetType, &x.TargetID, &x.Before, &x.After, &x.IP, &x.RequestID); err != nil {
			fail(w, r, err)
			return
		}
		items = append(items, x)
	}
	writeJSON(w, http.StatusOK, list[auditRow]{Items: items, Page: page, PageSize: size, Total: total})
}

// ---- Google sign-in (OpenID Connect authorization code flow) ----

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	oauthStateCookie  = "bm_oauth_state"
)

func (a *API) authProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"password": true, "google": a.cfg.GoogleSSO()})
}

func (a *API) googleRedirectURL() string { return a.publicOrigin + "/api/v1/auth/google/callback" }

func (a *API) googleStart(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.GoogleSSO() {
		fail(w, r, errNotFound)
		return
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state := base64.RawURLEncoding.EncodeToString(b)
	v := make([]byte, 32)
	_, _ = rand.Read(v)
	verifier := base64.RawURLEncoding.EncodeToString(v)
	sum := sha256.Sum256([]byte(verifier))
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: state + "." + verifier, Path: "/api/v1/auth/google",
		MaxAge: 600, HttpOnly: true, Secure: a.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	q := url.Values{
		"client_id": {a.cfg.GoogleClientID}, "redirect_uri": {a.googleRedirectURL()}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {state}, "prompt": {"select_account"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	if a.cfg.GoogleAllowedDomain != "" {
		q.Set("hd", a.cfg.GoogleAllowedDomain)
	}
	http.Redirect(w, r, googleAuthURL+"?"+q.Encode(), http.StatusFound)
}

func (a *API) googleCallback(w http.ResponseWriter, r *http.Request) {
	failRedirect := func(reason string) {
		loggerFrom(r.Context()).Warn("google sign-in refused", "reason", reason)
		http.Redirect(w, r, "/login?error=sso", http.StatusFound)
	}
	if !a.cfg.GoogleSSO() {
		fail(w, r, errNotFound)
		return
	}
	c, err := r.Cookie(oauthStateCookie)
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Path: "/api/v1/auth/google", MaxAge: -1, HttpOnly: true, Secure: a.cfg.SecureCookies()})
	if err != nil {
		failRedirect("missing state cookie")
		return
	}
	state, verifier, _ := strings.Cut(c.Value, ".")
	if state == "" || r.URL.Query().Get("state") != state || r.URL.Query().Get("code") == "" {
		failRedirect("state mismatch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	form := url.Values{"code": {r.URL.Query().Get("code")}, "client_id": {a.cfg.GoogleClientID}, "client_secret": {a.cfg.GoogleClientSecret},
		"redirect_uri": {a.googleRedirectURL()}, "grant_type": {"authorization_code"}, "code_verifier": {verifier}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		failRedirect("token exchange failed")
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		failRedirect(fmt.Sprintf("token endpoint HTTP %d", resp.StatusCode))
		return
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		failRedirect("userinfo failed")
		return
	}
	var info struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		HD            string `json:"hd"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || info.Email == "" || !info.EmailVerified {
		failRedirect("email missing or unverified")
		return
	}
	userID, err := a.auth.FindOrProvisionSSO(r.Context(), info.Email, info.Name, info.HD, a.cfg.GoogleAllowedDomain)
	if err != nil {
		failRedirect("no matching account for " + info.Email)
		return
	}
	token, u, err := a.auth.StartSession(r.Context(), userID, clientIP(r), r.UserAgent())
	if err != nil {
		failRedirect("session failed")
		return
	}
	a.setSessionCookies(w, token, a.auth.CSRFToken(u), a.auth.MaxAge())
	ev := a.event(r)
	ev.ActorID, ev.Action, ev.TargetType, ev.TargetID = u.ID, "user.login", "user", u.ID
	ev.After = map[string]string{"method": "google"}
	_ = recordAudit(r.Context(), a, ev)
	http.Redirect(w, r, "/", http.StatusFound)
}
