package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func (e *env) exporterURL() string { return fmt.Sprintf("http://127.0.0.1:%d/metrics", e.expPort) }

func (e *env) register(c *client, name string) string {
	e.t.Helper()
	r := c.do("POST", "/api/v1/servers", fmt.Sprintf(`{"name":%q,"exporter_url":%q,"environment":"production","labels":{"role":"worker"}}`, name, e.exporterURL()))
	if r.status != 201 {
		e.t.Fatalf("register %s = %d %s", name, r.status, r.raw)
	}
	return dig(r.body, "server", "id").(string)
}

func (e *env) targetsFile() []map[string]any {
	e.t.Helper()
	if _, err := e.targets.Sync(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	groups, _ := e.targets.Build(context.Background())
	raw, _ := json.Marshal(groups)
	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestRegisterServerAndTargets(t *testing.T) {
	e := newEnv(t)
	op := e.as("operator")

	// Validate without saving.
	r := op.do("POST", "/api/v1/servers/validate", fmt.Sprintf(`{"exporter_url":%q}`, e.exporterURL()))
	if r.status != 200 || dig(r.body, "validation", "node_exporter") != true || dig(r.body, "validation", "node_exporter_version") != "1.9.1" {
		t.Fatalf("validate = %d %s", r.status, r.raw)
	}

	id := e.register(op, "bm1")
	groups := e.targetsFile()
	if len(groups) != 1 {
		t.Fatalf("targets = %v", groups)
	}
	labels := groups[0]["labels"].(map[string]any)
	if labels["server_id"] != id || labels["server_name"] != "bm1" || labels["__scrape_interval__"] != "30s" {
		t.Errorf("target labels = %v", labels)
	}
	if tg := groups[0]["targets"].([]any)[0]; tg != fmt.Sprintf("127.0.0.1:%d", e.expPort) {
		t.Errorf("target = %v (must be the resolved IP)", tg)
	}

	// Duplicates: same name (any case) or same endpoint → 409.
	r = op.do("POST", "/api/v1/servers", fmt.Sprintf(`{"name":"BM1","exporter_url":%q}`, "http://10.0.0.99:9100/metrics"))
	if r.status != 422 && r.status != 409 {
		t.Fatalf("duplicate name = %d %s", r.status, r.raw)
	}
	r = op.do("POST", "/api/v1/servers", fmt.Sprintf(`{"name":"other","exporter_url":%q}`, e.exporterURL()))
	if r.status != 409 || field(r, "exporter_url") == "" {
		t.Fatalf("duplicate endpoint = %d %s", r.status, r.raw)
	}

	// Disable removes it from the targets; enable puts it back.
	if r := op.do("POST", "/api/v1/servers/"+id+"/disable", ""); r.status != 200 {
		t.Fatalf("disable = %d %s", r.status, r.raw)
	}
	if g := e.targetsFile(); len(g) != 0 {
		t.Fatalf("disabled server still a target: %v", g)
	}
	if r := op.do("POST", "/api/v1/servers/"+id+"/enable", ""); r.status != 200 {
		t.Fatalf("enable = %d", r.status)
	}
	if g := e.targetsFile(); len(g) != 1 {
		t.Fatalf("enabled server missing from targets")
	}

	// Activity periods were recorded for billing.
	var periods int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM server_activity WHERE server_id = $1`, id).Scan(&periods)
	if periods != 2 {
		t.Errorf("activity periods = %d, want 2 (created, re-enabled)", periods)
	}

	// Delete needs admin and the exact name.
	admin := e.as("admin")
	if r := admin.do("DELETE", "/api/v1/servers/"+id, `{"confirm_name":"bm2"}`); r.status != 400 {
		t.Fatalf("wrong confirm name = %d", r.status)
	}
	if r := admin.do("DELETE", "/api/v1/servers/"+id, `{"confirm_name":"bm1"}`); r.status != 204 {
		t.Fatalf("delete = %d %s", r.status, r.raw)
	}
	if g := e.targetsFile(); len(g) != 0 {
		t.Fatalf("deleted server still a target")
	}
	if r := admin.do("GET", "/api/v1/servers/"+id, ""); r.status != 404 {
		t.Errorf("deleted server still visible: %d", r.status)
	}
	// The name and endpoint are free again.
	e.register(op, "bm1")

	var audits int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE target_type = 'server'`).Scan(&audits)
	if audits < 5 {
		t.Errorf("audit events for servers = %d, want ≥5", audits)
	}
}

func TestSSRFGuard(t *testing.T) {
	e := newEnv(t)
	op := e.as("operator")
	cases := map[string]string{
		"http://169.254.169.254:9100/metrics": "forbidden range",
		"http://[::1]:9100/metrics":           "forbidden range",
		"http://192.168.1.10:9100/metrics":    "not in an allowed network",
		"http://10.0.0.5:22/metrics":          "not allowed",
		"ftp://10.0.0.5:9100/metrics":         "http or https",
		"http://user:pw@10.0.0.5:9100/":       "credentials",
		"http://10.0.0.5:9100/metrics?x=1":    "query",
		"http://no-such-host.invalid:9100/":   "could not be resolved",
	}
	for u, want := range cases {
		r := op.do("POST", "/api/v1/servers/validate", fmt.Sprintf(`{"exporter_url":%q}`, u))
		if r.status != 400 || !strings.Contains(field(r, "exporter_url"), want) {
			t.Errorf("%s → %d %q, want 400 containing %q", u, r.status, field(r, "exporter_url"), want)
		}
	}
}

func TestUnreachableEndpointNeedsAdminForce(t *testing.T) {
	e := newEnv(t)
	body := `{"name":"dead","exporter_url":"http://10.0.0.250:9100/metrics","force":true}`
	r := e.as("operator").do("POST", "/api/v1/servers", body)
	if r.status != 422 || errCode(r) != "endpoint_unreachable" {
		t.Fatalf("operator force = %d %s", r.status, r.raw)
	}
	r = e.as("admin").do("POST", "/api/v1/servers", body)
	if r.status != 201 || r.body["status"] != "unknown" {
		t.Fatalf("admin force = %d %s", r.status, r.raw)
	}
}

func TestListStatusesFromMetricsAndSorting(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	online := e.register(admin, "alpha")
	r := admin.do("POST", "/api/v1/servers", `{"name":"bravo","exporter_url":"http://10.0.0.2:9100/metrics","force":true}`)
	offline := dig(r.body, "server", "id").(string)
	r = admin.do("POST", "/api/v1/servers", `{"name":"charlie","exporter_url":"http://10.0.0.3:9100/metrics","force":true}`)
	unknown := dig(r.body, "server", "id").(string)
	r = admin.do("POST", "/api/v1/servers", `{"name":"delta","exporter_url":"http://10.0.0.4:9100/metrics","force":true}`)
	disabled := dig(r.body, "server", "id").(string)
	admin.do("POST", "/api/v1/servers/"+disabled+"/disable", "")

	now := float64(time.Now().Unix())
	lbl := func(id string) map[string]string { return map[string]string{"server_id": id} }
	e.prom.set("timestamp(up", fakeSeries{lbl(online), now}, fakeSeries{lbl(offline), now})
	e.prom.set(`up{job="node"}`, fakeSeries{lbl(online), 1}, fakeSeries{lbl(offline), 0})
	e.prom.set("node_cpu_seconds_total", fakeSeries{lbl(online), 0.42})

	r = admin.do("GET", "/api/v1/servers?sort=-cpu", "")
	if r.status != 200 || r.body["metrics_available"] != true {
		t.Fatalf("list = %d %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	status := map[string]string{}
	for _, it := range items {
		m := it.(map[string]any)
		status[m["name"].(string)] = m["status"].(string)
	}
	want := map[string]string{"alpha": "online", "bravo": "offline", "charlie": "unknown", "delta": "disabled"}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s status = %s, want %s", k, status[k], v)
		}
	}
	first := items[0].(map[string]any)
	if first["name"] != "alpha" || dig(first, "metrics", "cpu") != 0.42 {
		t.Errorf("-cpu sort: first = %v", first)
	}
	_ = unknown

	// Filters and paging.
	r = admin.do("GET", "/api/v1/servers?status=offline,unknown&page_size=1&page=2", "")
	if r.body["total"] != float64(2) || len(r.body["items"].([]any)) != 1 {
		t.Errorf("filter+paging = %s", r.raw)
	}
	r = admin.do("GET", "/api/v1/servers?label=role=worker", "")
	if r.body["total"] != float64(1) {
		t.Errorf("label filter total = %v", r.body["total"])
	}
	if r := admin.do("GET", "/api/v1/servers?sort=bogus", ""); r.status != 400 {
		t.Errorf("bad sort = %d", r.status)
	}

	// Fleet summary counts come from the inventory and metrics, never hardcoded.
	r = admin.do("GET", "/api/v1/fleet/summary", "")
	s := r.body["servers"].(map[string]any)
	if s["registered"] != float64(4) || s["online"] != float64(1) || s["offline"] != float64(1) || s["unknown"] != float64(1) ||
		s["disabled"] != float64(1) || s["enabled"] != float64(3) {
		t.Errorf("summary servers = %v", s)
	}
	if dig(r.body, "cost", "status") != "not_configured" {
		t.Errorf("cost without pricing = %v", r.body["cost"])
	}

	// Prometheus down: statuses become unknown, the API still answers.
	e.prom.setDown(true)
	time.Sleep(11 * time.Second) // let the 10 s snapshot cache expire
	r = admin.do("GET", "/api/v1/servers", "")
	if r.status != 200 || r.body["metrics_available"] != false {
		t.Fatalf("list with prometheus down = %d %s", r.status, r.raw)
	}
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if m["status"] != "unknown" && m["status"] != "disabled" {
			t.Errorf("%s = %s with Prometheus down", m["name"], m["status"])
		}
	}
}

func TestHistoryHasGapsAndBoundedRanges(t *testing.T) {
	e := newEnv(t)
	admin := e.as("admin")
	id := e.register(admin, "hist")
	e.prom.set("node_cpu_seconds_total", fakeSeries{map[string]string{"server_id": id}, 0.3})
	r := admin.do("GET", "/api/v1/servers/"+id+"/metrics?range=1h&series=cpu", "")
	if r.status != 200 || r.body["step_s"] != float64(15) {
		t.Fatalf("history = %d %s", r.status, r.raw)
	}
	pts := r.body["series"].([]any)[0].(map[string]any)["points"].([]any)
	if len(pts) < 200 || len(pts) > 500 {
		t.Errorf("points = %d, want a bounded grid", len(pts))
	}
	var nulls int
	for _, p := range pts {
		if p.([]any)[1] == nil {
			nulls++
		}
	}
	if nulls == 0 {
		t.Error("missing samples must be null, not dropped or zero")
	}
	if cov := r.body["coverage"].(float64); cov <= 0.3 || cov >= 0.7 {
		t.Errorf("coverage = %v, want ≈0.5", cov)
	}
	if r := admin.do("GET", "/api/v1/servers/"+id+"/metrics?range=90d", ""); r.status != 400 {
		t.Errorf("unbounded range = %d", r.status)
	}
}
