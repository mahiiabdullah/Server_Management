package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

func TestPricingAndCostSummary(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	id := e.register(admin, "costly")

	// No pricing yet: say so, never a number.
	r := admin.do("GET", "/api/v1/costs/summary", "")
	if r.body["status"] != "not_configured" || r.body["formula"] == nil {
		t.Fatalf("unconfigured summary = %s", r.raw)
	}

	r = admin.do("POST", "/api/v1/pricing-plans", `{"name":"BM large","amount":744,"unit":"month","category":"compute"}`)
	if r.status != 201 || r.body["currency"] != "USD" {
		t.Fatalf("create plan = %d %s", r.status, r.raw)
	}
	plan := r.body["id"].(string)
	if r := admin.do("POST", "/api/v1/pricing-plans", `{"name":"bad","amount":-1,"unit":"week"}`); r.status != 400 || field(r, "amount") == "" || field(r, "unit") == "" {
		t.Errorf("invalid plan = %d %s", r.status, r.raw)
	}

	// Assign from the start of the current month in the billing timezone (UTC here).
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	// Backdate the server's activity so the whole month counts.
	e.pool.Exec(context.Background(), `UPDATE server_activity SET active_from = $2 WHERE server_id = $1`, id, monthStart.AddDate(0, -1, 0))
	r = admin.do("PUT", "/api/v1/servers/"+id+"/pricing", `{"plan_id":"`+plan+`","effective_from":"`+monthStart.Format("2006-01-02")+`"}`)
	if r.status != 200 || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("assign = %d %s", r.status, r.raw)
	}

	// The plan is now locked: price changes need a new plan.
	if r := admin.do("PATCH", "/api/v1/pricing-plans/"+plan, `{"amount":999}`); r.status != 400 || field(r, "amount") == "" {
		t.Errorf("edit locked price = %d %s", r.status, r.raw)
	}
	if r := admin.do("PATCH", "/api/v1/pricing-plans/"+plan, `{"name":"BM large v1"}`); r.status != 200 {
		t.Errorf("rename locked plan = %d %s", r.status, r.raw)
	}

	r = admin.do("GET", "/api/v1/costs/summary", "")
	if r.body["status"] != "configured" || r.body["kind"] != "estimated" {
		t.Fatalf("summary = %s", r.raw)
	}
	projected := dig(r.body, "result", "projected").(float64)
	if projected != 744 {
		t.Errorf("projected = %v, want 744 (full month at 744/month)", projected)
	}
	toDate := dig(r.body, "result", "to_date").(float64)
	elapsedH := now.Sub(monthStart).Hours()
	hoursInMonth := monthStart.AddDate(0, 1, 0).Sub(monthStart).Hours()
	if want := 744 * elapsedH / hoursInMonth; toDate < want-1 || toDate > want+1 {
		t.Errorf("to date = %v, want ≈%.2f", toDate, want)
	}

	// Fleet summary now shows cost.
	r = admin.do("GET", "/api/v1/fleet/summary", "")
	if dig(r.body, "cost", "status") != "configured" || dig(r.body, "cost", "projected") != float64(744) {
		t.Errorf("fleet cost = %v", r.body["cost"])
	}

	// Trend includes the current month with a projection.
	r = admin.do("GET", "/api/v1/costs/trend?months=3", "")
	pts := r.body["points"].([]any)
	if len(pts) != 3 || pts[2].(map[string]any)["projected"] != float64(744) || pts[2].(map[string]any)["actual"] != nil {
		t.Errorf("trend = %s", r.raw)
	}

	// CSV import of actual invoices: all-or-nothing validation.
	upload := func(csv string) apiResp {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "invoice.csv")
		io.WriteString(fw, csv)
		mw.Close()
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/costs/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-CSRF-Token", admin.csrf)
		resp, err := admin.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := apiResp{status: resp.StatusCode}
		out.raw, _ = io.ReadAll(resp.Body)
		return out
	}
	period := now.Format("2006-01")
	if r := upload("server,period,amount\ncostly," + period + ",700\nghost," + period + ",5\n"); r.status != 422 {
		t.Errorf("import with unknown server = %d %s", r.status, r.raw)
	}
	if r := upload("server,period,amount,currency\ncostly," + period + ",700.50,USD\n"); r.status != 200 {
		t.Fatalf("import = %d %s", r.status, r.raw)
	}
	r = admin.do("GET", "/api/v1/costs/summary", "")
	if dig(r.body, "actual", "total") != 700.5 {
		t.Errorf("actual = %v", r.body["actual"])
	}
}

func TestAlertLifecycleEndToEnd(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	id := e.register(admin, "hot")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	prom := metrics.NewClient(e.promURL)
	inv := inventory.NewStore(e.pool)
	ev := alerts.NewEvaluator(e.pool, prom, inv, cost.NewStore(e.pool, time.UTC, "USD", log), log)
	ctx := context.Background()

	// An email channel so notifications are queued (the notifier is not run here).
	r := admin.do("POST", "/api/v1/notification-channels", `{"kind":"email","name":"ops","config":{"to":["ops@example.com"]},"min_severity":"warning"}`)
	if r.status != 201 || r.body["configured"] != false {
		t.Fatalf("channel = %d %s (configured must be false without SMTP env)", r.status, r.raw)
	}
	if r := admin.do("POST", "/api/v1/notification-channels", `{"kind":"email","name":"bad","config":{"to":["not-an-address"]}}`); r.status != 400 {
		t.Errorf("bad recipient = %d", r.status)
	}

	// Make the CPU rule fire immediately for this test.
	r = admin.do("GET", "/api/v1/alert-rules", "")
	var cpuRule string
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if m["kind"] == "cpu" {
			cpuRule = m["id"].(string)
		}
	}
	if r := admin.do("PATCH", "/api/v1/alert-rules/"+cpuRule, `{"name":"High CPU","threshold":90,"for_seconds":0,"severity":"warning"}`); r.status != 200 {
		t.Fatalf("edit rule = %d %s", r.status, r.raw)
	}

	now := float64(time.Now().Unix())
	lbl := map[string]string{"server_id": id}
	e.prom.set("timestamp(up", fakeSeries{lbl, now})
	e.prom.set(`up{job="node"}`, fakeSeries{lbl, 1})
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl, 0.97})

	countNotif := func(event string) int {
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event = $1`, event).Scan(&n)
		return n
	}
	for i := 0; i < 3; i++ { // repeated evaluations must not duplicate the incident or the notification
		if err := ev.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r = admin.do("GET", "/api/v1/alerts?state=firing", "")
	if r.body["total"] != float64(1) {
		t.Fatalf("firing alerts = %s", r.raw)
	}
	alertID := r.body["items"].([]any)[0].(map[string]any)["id"].(string)
	if countNotif("fired") != 1 {
		t.Errorf("fired notifications = %d, want exactly 1", countNotif("fired"))
	}

	// Acknowledge (operator) stops reminders.
	if r := e.as("operator").do("POST", "/api/v1/alerts/"+alertID+"/ack", ""); r.status != 204 {
		t.Fatalf("ack = %d %s", r.status, r.raw)
	}
	e.pool.Exec(ctx, `UPDATE alerts SET last_notified_at = now() - interval '1 day' WHERE id = $1`, alertID)
	ev.Evaluate(ctx)
	if countNotif("reminder") != 0 {
		t.Error("reminder sent for an acknowledged alert")
	}

	// Recovery: two clean checks resolve it and queue one "resolved".
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl, 0.2})
	ev.Evaluate(ctx)
	r = admin.do("GET", "/api/v1/alerts?state=resolved", "")
	if r.body["total"] != float64(0) {
		t.Fatal("resolved after a single clean check (flap guard missing)")
	}
	ev.Evaluate(ctx)
	r = admin.do("GET", "/api/v1/alerts?state=resolved", "")
	if r.body["total"] != float64(1) || countNotif("resolved") != 1 {
		t.Fatalf("resolve = %s, resolved notifications %d", r.raw, countNotif("resolved"))
	}

	// A condition that clears while pending never becomes a visible incident.
	e.pool.Exec(ctx, `UPDATE alert_rules SET for_seconds = 3600 WHERE id = $1`, cpuRule)
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl, 0.99})
	ev.Evaluate(ctx) // pending
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl, 0.1})
	ev.Evaluate(ctx) // cleared while pending
	r = admin.do("GET", "/api/v1/alerts?state=resolved", "")
	if r.body["total"] != float64(1) {
		t.Fatalf("a never-fired pending alert is listed as an incident: %s", r.raw)
	}

	// Prometheus down: one "monitoring unavailable" alert, not an unreachable storm.
	e.prom.setDown(true)
	e.pool.Exec(ctx, `UPDATE servers SET created_at = now() - interval '1 hour'`)
	e.pool.Exec(ctx, `UPDATE alert_rules SET for_seconds = 0`)
	ev.Evaluate(ctx)
	r = admin.do("GET", "/api/v1/alerts?state=firing", "")
	items := r.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "monitoring" {
		t.Fatalf("with Prometheus down, firing = %s", r.raw)
	}

	// System rules cannot be deleted.
	monitoringRule := items[0].(map[string]any)["rule_id"].(string)
	if r := admin.do("DELETE", "/api/v1/alert-rules/"+monitoringRule, ""); r.status != 409 {
		t.Errorf("delete system rule = %d", r.status)
	}
}

func TestUnreachableServerAlert(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	r := admin.do("POST", "/api/v1/servers", `{"name":"gone","exporter_url":"http://10.0.0.77:9100/metrics","force":true}`)
	id := dig(r.body, "server", "id").(string)
	ctx := context.Background()
	e.pool.Exec(ctx, `UPDATE servers SET created_at = now() - interval '1 hour'`)
	e.pool.Exec(ctx, `UPDATE alert_rules SET for_seconds = 0 WHERE kind = 'unreachable'`)
	lbl := map[string]string{"server_id": id}
	e.prom.set("timestamp(up", fakeSeries{lbl, float64(time.Now().Unix())})
	e.prom.set(`up{job="node"}`, fakeSeries{lbl, 0})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ev := alerts.NewEvaluator(e.pool, metrics.NewClient(e.promURL), inventory.NewStore(e.pool), cost.NewStore(e.pool, time.UTC, "USD", log), log)
	if err := ev.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	r = admin.do("GET", "/api/v1/alerts?state=firing&server="+id, "")
	if r.body["total"] != float64(1) || r.body["items"].([]any)[0].(map[string]any)["severity"] != "critical" {
		t.Fatalf("unreachable alert = %s", r.raw)
	}
	// Disabling the server closes its incident.
	admin.do("POST", "/api/v1/servers/"+id+"/disable", "")
	r = admin.do("GET", "/api/v1/alerts?state=firing,acknowledged&server="+id, "")
	if r.body["total"] != float64(0) {
		t.Errorf("incident left open after disable: %s", r.raw)
	}
}

func TestSettingsAndAudit(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	if r := admin.do("PUT", "/api/v1/settings", `{"billing_currency":"EUR"}`); r.status != 400 {
		t.Errorf("env-only setting accepted: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings", `{"underutil_cpu_p95":500}`); r.status != 400 {
		t.Errorf("out-of-range accepted: %d", r.status)
	}
	r := admin.do("PUT", "/api/v1/settings", `{"proration":{"enabled":false},"underutil_cpu_p95":15}`)
	if r.status != 200 || dig(r.body, "values", "underutil_cpu_p95") != float64(15) {
		t.Fatalf("settings = %d %s", r.status, r.raw)
	}
	r = admin.do("GET", "/api/v1/audit?action=settings", "")
	if r.body["total"] != float64(2) {
		t.Errorf("settings audit = %s", r.raw)
	}
}
