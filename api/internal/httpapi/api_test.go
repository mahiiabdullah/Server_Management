package httpapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"bm-monitor/api/internal/auth"
)

func TestHealthAndReadiness(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if r := c.do("GET", "/healthz", ""); r.status != 200 {
		t.Fatalf("healthz = %d", r.status)
	}
	r := c.do("GET", "/api/v1/readyz", "")
	if r.status != 200 || r.body["status"] != "ready" {
		t.Fatalf("readyz = %d %v", r.status, r.body)
	}
	if r.header.Get("X-Request-Id") == "" {
		t.Error("no X-Request-Id header")
	}
	e.prom.setDown(true)
	r = c.do("GET", "/readyz", "")
	if r.status != 503 || r.body["status"] != "not_ready" {
		t.Fatalf("readyz with Prometheus down = %d %v", r.status, r.body)
	}
	if strings.Contains(string(r.raw), "127.0.0.1") {
		t.Errorf("readyz leaks internal address: %s", r.raw)
	}
}

func TestLoginMeLogout(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if r := c.do("GET", "/api/v1/me", ""); r.status != 401 || errCode(r) != "unauthenticated" {
		t.Fatalf("me before login = %d %v", r.status, r.body)
	}
	if r := c.login("ADMIN@example.com", adminPass); r.status != 200 || c.csrf == "" {
		t.Fatalf("login = %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/api/v1/me", ""); r.status != 200 || dig(r.body, "user", "role") != "admin" {
		t.Fatalf("me after login = %d %v", r.status, r.body)
	}
	if r := c.doHdr("POST", "/api/v1/auth/logout", "", map[string]string{"X-CSRF-Token": ""}); r.status != 403 || errCode(r) != "csrf_failed" {
		t.Fatalf("logout without csrf = %d %v", r.status, r.body)
	}
	if r := c.doHdr("POST", "/api/v1/auth/logout", "", map[string]string{"Origin": "http://evil.test"}); r.status != 403 || errCode(r) != "bad_origin" {
		t.Fatalf("logout from foreign origin = %d %v", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/auth/logout", ""); r.status != 204 {
		t.Fatalf("logout = %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/api/v1/me", ""); r.status != 401 {
		t.Fatalf("me after logout = %d", r.status)
	}
	var n int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'user.login'`).Scan(&n)
	if n != 1 {
		t.Errorf("audit login events = %d, want 1", n)
	}
}

func TestLoginValidationAndErrors(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	r := c.do("POST", "/api/v1/auth/login", `{"email":"nope","password":""}`)
	if r.status != 400 || field(r, "email") == "" || field(r, "password") == "" {
		t.Fatalf("bad input = %d %v", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/auth/login", `{"email":"a@b.c","password":"x","extra":1}`); errCode(r) != "invalid_json" {
		t.Errorf("unknown field = %d %v", r.status, r.body)
	}
	if r := c.login("unknown@example.com", "whatever"); r.status != 401 || errCode(r) != "invalid_credentials" {
		t.Errorf("unknown user = %d %v", r.status, r.body)
	}
	if r := c.do("GET", "/api/v1/nope", ""); r.status != 401 && r.status != 404 {
		t.Errorf("unknown route = %d %v", r.status, r.body)
	}
}

func TestAccountLockoutAndIPLimit(t *testing.T) {
	e := newEnv(t)
	var last apiResp
	for i := 0; i < auth.MaxFailedLogins; i++ {
		last = e.client().login("admin@example.com", "wrong-password")
	}
	if last.status != 429 || errCode(last) != "account_locked" {
		t.Fatalf("5th failure = %d %v", last.status, last.body)
	}
	e.pool.Exec(context.Background(), `UPDATE users SET locked_until = NULL`)
	if r := e.client().login("admin@example.com", adminPass); r.status != 429 || errCode(r) != "rate_limited" {
		t.Fatalf("6th attempt from same IP = %d %v", r.status, r.body)
	}
}

func TestLockedAccountRejectsCorrectPassword(t *testing.T) {
	e := newEnv(t)
	e.pool.Exec(context.Background(), `UPDATE users SET locked_until = now() + interval '10 minutes'`)
	r := e.client().login("admin@example.com", adminPass)
	if r.status != 429 || errCode(r) != "account_locked" || r.header.Get("Retry-After") == "" {
		t.Fatalf("locked login = %d %v", r.status, r.body)
	}
}

func TestDisabledUserLosesSession(t *testing.T) {
	e := newEnv(t)
	c := e.as("viewer")
	if r := c.do("GET", "/api/v1/me", ""); r.status != 200 {
		t.Fatalf("viewer me = %d", r.status)
	}
	e.pool.Exec(context.Background(), `UPDATE users SET disabled = true WHERE email = 'viewer@example.com'`)
	if r := c.do("GET", "/api/v1/me", ""); r.status != 401 {
		t.Fatalf("disabled user me = %d", r.status)
	}
}

func TestRolesEnforcedOnEveryProtectedAction(t *testing.T) {
	e := newEnv(t)
	viewer, operator := e.as("viewer"), e.as("operator")
	id := "00000000-0000-4000-8000-000000000000"
	// (client, method, path, body, want status)
	cases := []struct {
		c      *client
		method string
		path   string
		want   int
	}{
		{viewer, "POST", "/api/v1/servers", 403},
		{viewer, "POST", "/api/v1/servers/validate", 403},
		{viewer, "POST", "/api/v1/servers/" + id + "/disable", 403},
		{viewer, "POST", "/api/v1/alerts/" + id + "/ack", 403},
		{operator, "DELETE", "/api/v1/servers/" + id, 403},
		{operator, "POST", "/api/v1/pricing-plans", 403},
		{operator, "PUT", "/api/v1/servers/" + id + "/pricing", 403},
		{operator, "POST", "/api/v1/alert-rules", 403},
		{operator, "GET", "/api/v1/users", 403},
		{operator, "GET", "/api/v1/audit", 403},
		{operator, "PUT", "/api/v1/settings", 403},
		{operator, "GET", "/api/v1/notification-channels", 403},
		{e.client(), "GET", "/api/v1/servers", 401},
		{e.client(), "GET", "/api/v1/fleet/summary", 401},
	}
	for _, tc := range cases {
		if r := tc.c.do(tc.method, tc.path, "{}"); r.status != tc.want {
			t.Errorf("%s %s = %d, want %d (%s)", tc.method, tc.path, r.status, tc.want, r.raw)
		}
	}
}

func TestSessionTokenIsStoredHashed(t *testing.T) {
	e := newEnv(t)
	c := e.as("admin")
	var token string
	u, _ := url.Parse(e.srv.URL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "bm_session" {
			token = ck.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie")
	}
	var n int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE token_hash = convert_to($1, 'UTF8')`, token).Scan(&n)
	if n != 0 {
		t.Error("raw session token found in the database")
	}
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	e := newEnv(t)
	a, b := e.as("admin"), e.as("admin")
	r := a.do("POST", "/api/v1/me/password", `{"current_password":"wrong","new_password":"a-brand-new-password"}`)
	if r.status != 400 || field(r, "current_password") == "" {
		t.Fatalf("wrong current = %d %v", r.status, r.body)
	}
	if r := a.do("POST", "/api/v1/me/password", `{"current_password":"`+adminPass+`","new_password":"a-brand-new-password"}`); r.status != 204 {
		t.Fatalf("change = %d %v", r.status, r.body)
	}
	if r := a.do("GET", "/api/v1/me", ""); r.status != 200 {
		t.Error("own session ended")
	}
	if r := b.do("GET", "/api/v1/me", ""); r.status != 401 {
		t.Error("other session survived a password change")
	}
}

func TestUsersAndLastAdmin(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	r := admin.do("POST", "/api/v1/users", `{"email":"ops@example.com","name":"Ops","role":"operator","password":"operator-password"}`)
	if r.status != 201 {
		t.Fatalf("create user = %d %v", r.status, r.body)
	}
	if r := admin.do("POST", "/api/v1/users", `{"email":"OPS@example.com","name":"Ops","role":"operator","password":"operator-password"}`); r.status != 409 {
		t.Errorf("duplicate email = %d", r.status)
	}
	me := admin.do("GET", "/api/v1/me", "")
	myID := dig(me.body, "user", "id").(string)
	if r := admin.do("PATCH", "/api/v1/users/"+myID, `{"role":"viewer"}`); r.status != 400 {
		t.Errorf("self-demote = %d %v", r.status, r.body)
	}
	raw, _ := json.Marshal(r.body)
	if !strings.Contains(string(raw), "has_password") {
		t.Errorf("account shape: %s", raw)
	}
}
