package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

func (e *env) person(c *client, body string) string {
	e.t.Helper()
	r := c.do("POST", "/api/v1/users", body)
	if r.status != 201 {
		e.t.Fatalf("create person %s = %d %s", body, r.status, r.raw)
	}
	return r.body["id"].(string)
}

// recipients lists who an alert event was queued for: person names and channel names.
func (e *env) recipients(event string) []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT coalesce(u.name, c.name) FROM notifications n
		LEFT JOIN users u ON u.id = n.user_id LEFT JOIN notification_channels c ON c.id = n.channel_id WHERE n.event = $1`, event)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Alerts go to the people tagged with one of the server's groups, to "All alerts"
// people, and to unscoped or matching team mailboxes; never twice to one address.
func TestAlertsGoToPeopleByGroup(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	ctx := context.Background()
	bot, chat := e.group(admin, "puku-bot"), e.group(admin, "puku-web-chat")
	r := admin.do("POST", "/api/v1/servers", fmt.Sprintf(`{"name":"hot","exporter_url":%q,"group_ids":[%q]}`, e.exporterURL(), bot))
	if r.status != 201 {
		t.Fatalf("server = %d %s", r.status, r.raw)
	}
	server := dig(r.body, "server", "id").(string)

	contact := func(name, extra string) string {
		return e.person(admin, fmt.Sprintf(`{"kind":"contact","name":%q,"email":"%s@example.com"%s}`, name, strings.ToLower(name), extra))
	}
	alice := contact("Alice", fmt.Sprintf(`,"alert_group_ids":[%q]`, bot))
	contact("Bob", fmt.Sprintf(`,"alert_group_ids":[%q]`, chat))                                 // other group
	contact("Carol", `,"alerts_all":true`)                                                       // everything
	contact("Dave", fmt.Sprintf(`,"alert_group_ids":[%q],"alert_min_severity":"critical"`, bot)) // severity too high
	erin := contact("Erin", fmt.Sprintf(`,"alert_group_ids":[%q]`, bot))
	e.pool.Exec(ctx, `UPDATE users SET alerts_paused_until = now() + interval '1 day' WHERE id = $1`, erin) // paused
	e.person(admin, fmt.Sprintf(`{"kind":"contact","name":"Ops person","email":"ops@example.com","alert_group_ids":[%q]}`, bot))
	// Frank is a dashboard user who mutes the group himself.
	e.person(admin, fmt.Sprintf(`{"name":"Frank","email":"frank@example.com","role":"viewer","password":"frank-password-1","alert_group_ids":[%q]}`, bot))
	frank := e.client()
	if r := frank.login("frank@example.com", "frank-password-1"); r.status != 200 {
		t.Fatalf("frank login = %d", r.status)
	}
	if r := frank.do("PATCH", "/api/v1/me/alerts", fmt.Sprintf(`{"muted":{%q:true}}`, bot)); r.status != 200 {
		t.Fatalf("mute = %d %s", r.status, r.raw)
	}
	// People cannot mute (or join) groups an admin did not put them in.
	if r := frank.do("PATCH", "/api/v1/me/alerts", fmt.Sprintf(`{"muted":{%q:false}}`, chat)); r.status != 400 {
		t.Errorf("mute a foreign group = %d", r.status)
	}

	// Team mailboxes: one for everything, one for the bot group, one for the chat group.
	for _, body := range []string{
		`{"kind":"email","name":"ops box","config":{"to":["ops@example.com"]}}`,
		fmt.Sprintf(`{"kind":"email","name":"bot box","config":{"to":["bot@example.com"]},"group_ids":[%q]}`, bot),
		fmt.Sprintf(`{"kind":"email","name":"chat box","config":{"to":["chat@example.com"]},"group_ids":[%q]}`, chat),
	} {
		if r := admin.do("POST", "/api/v1/notification-channels", body); r.status != 201 {
			t.Fatalf("channel %s = %d %s", body, r.status, r.raw)
		}
	}

	// Fire a CPU alert on the server.
	e.pool.Exec(ctx, `UPDATE alert_rules SET for_seconds = 0, threshold = 90, severity = 'warning' WHERE kind = 'cpu'`)
	lbl := map[string]string{"server_id": server}
	e.prom.set("timestamp(up", fakeSeries{lbl, float64(time.Now().Unix())})
	e.prom.set(`up{job="node"}`, fakeSeries{lbl, 1})
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl, 0.97})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ev := alerts.NewEvaluator(e.pool, metrics.NewClient(e.promURL), inventory.NewStore(e.pool), cost.NewStore(e.pool, time.UTC, "USD", log), log)
	if err := ev.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	// Ops person's address is already on the ops box: no second email.
	if got := strings.Join(e.recipients("fired"), ","); got != "Alice,Carol,bot box,ops box" {
		t.Fatalf("fired recipients = %s", got)
	}

	// The delivery log names every recipient.
	r = admin.do("GET", "/api/v1/alerts?state=firing", "")
	alertID := r.body["items"].([]any)[0].(map[string]any)["id"].(string)
	if d := admin.do("GET", "/api/v1/alerts/"+alertID+"/deliveries", ""); d.status != 200 || len(d.body["items"].([]any)) != 4 {
		t.Errorf("deliveries = %d %s", d.status, d.raw)
	}

	// Resolution goes to the people who were told it fired, even if they left the group since.
	admin.do("PATCH", "/api/v1/users/"+alice, `{"alert_group_ids":[]}`)
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl, 0.1})
	ev.Evaluate(ctx)
	ev.Evaluate(ctx)
	if got := strings.Join(e.recipients("resolved"), ","); got != "Alice,Carol,bot box,ops box" {
		t.Fatalf("resolved recipients = %s", got)
	}
}

func TestContactsAndPeopleAdmin(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	bot := e.group(admin, "puku-bot")

	// Contacts never have a password or a role, and can never sign in.
	if r := admin.do("POST", "/api/v1/users", `{"kind":"contact","name":"M","email":"m@example.com","password":"long-enough-password"}`); r.status != 400 {
		t.Errorf("contact with password = %d", r.status)
	}
	m := e.person(admin, fmt.Sprintf(`{"kind":"contact","name":"Manager","email":"m@example.com","alert_group_ids":[%q]}`, bot))
	if r := admin.do("PATCH", "/api/v1/users/"+m, `{"role":"admin"}`); r.status != 400 {
		t.Errorf("promote contact = %d", r.status)
	}
	if r := admin.do("PATCH", "/api/v1/users/"+m, `{"password":"long-enough-password"}`); r.status != 400 {
		t.Errorf("contact password = %d", r.status)
	}
	if r := e.client().login("m@example.com", "anything-at-all"); r.status != 401 {
		t.Errorf("contact login = %d", r.status)
	}
	var kind string
	e.pool.QueryRow(context.Background(), `SELECT kind FROM users WHERE id = $1`, m).Scan(&kind)
	if kind != "contact" {
		t.Fatalf("kind = %s", kind)
	}

	// The users list shows each person's alert groups.
	r := admin.do("GET", "/api/v1/users", "")
	for _, it := range r.body["items"].([]any) {
		p := it.(map[string]any)
		if p["id"] == m && (p["kind"] != "contact" || len(p["alert_groups"].([]any)) != 1) {
			t.Errorf("listed contact = %v", p)
		}
	}

	// Group people: only admins change them; everyone sees names, only admins see addresses.
	viewer := e.as("viewer")
	if r := e.as("operator").do("PUT", "/api/v1/groups/"+bot+"/people", `{"user_ids":[]}`); r.status != 403 {
		t.Errorf("operator sets people = %d", r.status)
	}
	other := e.person(admin, `{"kind":"contact","name":"Second","email":"s@example.com"}`)
	if r := admin.do("PUT", "/api/v1/groups/"+bot+"/people", fmt.Sprintf(`{"user_ids":[%q,%q]}`, m, other)); r.status != 200 || len(r.body["people"].([]any)) != 2 {
		t.Fatalf("set people = %d %s", r.status, r.raw)
	}
	people := viewer.do("GET", "/api/v1/groups/"+bot, "").body["people"].([]any)
	if len(people) != 2 || people[0].(map[string]any)["email"] != nil {
		t.Errorf("viewer sees people = %v", people)
	}
	if p := admin.do("GET", "/api/v1/groups/"+bot, "").body["people"].([]any); p[0].(map[string]any)["email"] == nil {
		t.Errorf("admin does not see addresses: %v", p)
	}
	if r := admin.do("PUT", "/api/v1/groups/"+bot+"/people", `{"user_ids":["00000000-0000-0000-0000-000000000000"]}`); r.status != 400 {
		t.Errorf("unknown person = %d", r.status)
	}

	// Signed pause links work without signing in, and cannot be forged.
	links := alerts.NewLinks(e.cfg.SessionSecret)
	tok := links.Sign(m, alerts.LinkPause7d, time.Now().Add(time.Hour))
	anon := e.client()
	if r := anon.do("GET", "/api/v1/alert-links?t="+tok, ""); r.status != 200 || r.body["name"] != "Manager" || r.body["action"] != "pause7d" {
		t.Fatalf("link info = %d %s", r.status, r.raw)
	}
	r = anon.do("POST", "/api/v1/alert-links", fmt.Sprintf(`{"t":%q}`, tok))
	until, _ := time.Parse(time.RFC3339, fmt.Sprint(r.body["paused_until"]))
	if r.status != 200 || time.Until(until) < 6*24*time.Hour {
		t.Fatalf("pause = %d %s", r.status, r.raw)
	}
	for name, bad := range map[string]string{
		"tampered": strings.Replace(tok, tok[:4], "AAAA", 1),
		"expired":  links.Sign(m, alerts.LinkPause1d, time.Now().Add(-time.Minute)),
		"garbage":  "nope",
	} {
		if r := anon.do("POST", "/api/v1/alert-links", fmt.Sprintf(`{"t":%q}`, bad)); r.status != 400 {
			t.Errorf("%s link = %d", name, r.status)
		}
	}
	resume := links.Sign(m, alerts.LinkResume, time.Now().Add(time.Hour))
	if r := anon.do("POST", "/api/v1/alert-links", fmt.Sprintf(`{"t":%q}`, resume)); r.status != 200 || r.body["paused_until"] != nil {
		t.Errorf("resume = %d %s", r.status, r.raw)
	}

	// Disabled people cannot use their links.
	admin.do("PATCH", "/api/v1/users/"+m, `{"disabled":true}`)
	if r := anon.do("POST", "/api/v1/alert-links", fmt.Sprintf(`{"t":%q}`, resume)); r.status != 400 {
		t.Errorf("disabled person's link = %d", r.status)
	}
}
