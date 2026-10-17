package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"time"

	"golang.org/x/sync/errgroup"

	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/auth"
)

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type componentStatus struct {
	Status    string `json:"status"` // "ok" or "error"
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// readyz checks PostgreSQL and Prometheus in parallel, each with a 2 s timeout.
func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]func(context.Context) error{
		"database":   a.pool.Ping,
		"prometheus": a.prom.Ready,
	}
	results := make(map[string]componentStatus, len(checks))
	type res struct {
		name string
		st   componentStatus
	}
	ch := make(chan res, len(checks))
	var g errgroup.Group
	for name, check := range checks {
		g.Go(func() error {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			st := componentStatus{Status: "ok"}
			if err := check(ctx); err != nil {
				// Details go to the log only; the response must not leak internal addresses.
				loggerFrom(r.Context()).Warn("readiness check failed", "component", name, "err", err)
				st = componentStatus{Status: "error", Error: "unavailable"}
			}
			st.LatencyMS = time.Since(start).Milliseconds()
			ch <- res{name, st}
			return nil
		})
	}
	_ = g.Wait()
	close(ch)
	ready := true
	for x := range ch {
		results[x.name] = x.st
		ready = ready && x.st.Status == "ok"
	}
	status, code := "ready", http.StatusOK
	if !ready {
		status, code = "not_ready", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "components": results})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type meResponse struct {
	User      *auth.User `json:"user"`
	CSRFToken string     `json:"csrf_token"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	if _, err := mail.ParseAddress(req.Email); err != nil || len(req.Email) > 254 {
		fields["email"] = "Enter a valid email address."
	}
	if req.Password == "" || len(req.Password) > 1024 {
		fields["password"] = "Enter your password."
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	ip := clientIP(r)
	token, u, err := a.auth.Login(r.Context(), req.Email, req.Password, ip, r.UserAgent())
	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		mins := int(math.Ceil(time.Until(locked.Until).Minutes()))
		w.Header().Set("Retry-After", fmt.Sprint(mins*60))
		writeError(w, r, &APIError{Status: http.StatusTooManyRequests, Code: "account_locked",
			Message: fmt.Sprintf("Too many failed sign-in attempts. Try again in %d minute(s).", mins)})
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		loggerFrom(r.Context()).Warn("login failed", "ip", ip.String())
		writeError(w, r, &APIError{Status: http.StatusUnauthorized, Code: "invalid_credentials", Message: "Email or password is incorrect."})
		return
	case err != nil:
		writeError(w, r, err)
		return
	}

	a.setSessionCookies(w, token, a.auth.CSRFToken(u), a.auth.MaxAge())
	if err := audit.Record(r.Context(), a.pool, audit.Event{
		ActorID: u.ID, Action: "user.login", TargetType: "user", TargetID: u.ID, IP: ip, RequestID: requestIDFrom(r.Context()),
	}); err != nil {
		loggerFrom(r.Context()).Error("audit write failed", "err", err)
	}
	writeJSON(w, http.StatusOK, meResponse{User: u, CSRFToken: a.auth.CSRFToken(u)})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.Logout(r.Context(), auth.UserFrom(r.Context())); err != nil {
		writeError(w, r, err)
		return
	}
	a.setSessionCookies(w, "", "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{User: u, CSRFToken: a.auth.CSRFToken(u)})
}

// setSessionCookies writes the HttpOnly session cookie and the JS-readable CSRF
// cookie (double submit). A negative maxAge clears both.
func (a *API) setSessionCookies(w http.ResponseWriter, token, csrf string, maxAge time.Duration) {
	age := int(maxAge.Seconds())
	if maxAge < 0 {
		age = -1
	}
	secure := a.cfg.SecureCookies()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: age,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: "/", MaxAge: age,
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode})
}
