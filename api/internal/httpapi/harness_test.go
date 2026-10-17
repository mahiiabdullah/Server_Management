package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/config"
	"bm-monitor/api/internal/db"
	"bm-monitor/api/internal/dbtest"
	"bm-monitor/api/internal/httpapi"
	"bm-monitor/api/internal/metrics"
	"bm-monitor/api/internal/targets"
)

func TestMain(m *testing.M) {
	code := m.Run()
	dbtest.StopEmbedded()
	os.Exit(code)
}

// fakeProm speaks enough of the Prometheus HTTP API for the tests. A query is
// answered with the series of the first registered rule whose key it contains.
type fakeProm struct {
	mu    sync.Mutex
	down  bool
	rules []fakeRule
}

type fakeRule struct {
	contains string
	series   []fakeSeries
}

type fakeSeries struct {
	labels map[string]string
	value  float64
}

func (f *fakeProm) set(contains string, series ...fakeSeries) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rules {
		if r.contains == contains {
			f.rules[i].series = series
			return
		}
	}
	f.rules = append(f.rules, fakeRule{contains, series})
}

func (f *fakeProm) setDown(d bool) { f.mu.Lock(); f.down = d; f.mu.Unlock() }

func (f *fakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/-/ready":
		fmt.Fprint(w, "ready")
		return
	case "/api/v1/query", "/api/v1/query_range":
	default:
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	q := r.Form.Get("query")
	var match []fakeSeries
	for _, rule := range f.rules {
		if strings.Contains(q, rule.contains) {
			match = rule.series
			break
		}
	}
	now := float64(time.Now().Unix())
	var result []map[string]any
	for _, s := range match {
		if r.URL.Path == "/api/v1/query" {
			result = append(result, map[string]any{"metric": s.labels, "value": []any{now, fmt.Sprint(s.value)}})
		} else {
			start, _ := parseFloat(r.Form.Get("start"))
			step, _ := parseFloat(r.Form.Get("step"))
			// Return only every other point so the grid has gaps.
			var vals []any
			for t, i := start, 0; t <= now; t, i = t+step, i+1 {
				if i%2 == 0 {
					vals = append(vals, []any{t, fmt.Sprint(s.value)})
				}
			}
			result = append(result, map[string]any{"metric": s.labels, "values": vals})
		}
	}
	if result == nil {
		result = []map[string]any{}
	}
	rt := "vector"
	if r.URL.Path == "/api/v1/query_range" {
		rt = "matrix"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": rt, "result": result}})
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscan(s, &f)
	return f, err
}

// fakeExporter serves a Node Exporter-like /metrics page.
func fakeExporter(t *testing.T) (*httptest.Server, uint16) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "# HELP node_exporter_build_info\nnode_exporter_build_info{version=\"1.9.1\"} 1\nnode_load1 0.5\n")
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	ap := netip.MustParseAddrPort(u.Host)
	return srv, ap.Port()
}

type env struct {
	t       *testing.T
	srv     *httptest.Server
	pool    *pgxpool.Pool
	prom    *fakeProm
	targets *targets.Writer
	cfg     *config.Config
	expPort uint16
	promURL string
}

const adminPass = "admin-password-123"

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.NewTestDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	if err := auth.EnsureAdmin(ctx, pool, "admin@example.com", adminPass, log); err != nil {
		t.Fatal(err)
	}
	_, expPort := fakeExporter(t)
	cfg, err := config.LoadFrom(func(k string) string {
		return map[string]string{
			"DATABASE_URL": "unused", "SESSION_SECRET": strings.Repeat("s", 32), "CSRF_SECRET": strings.Repeat("c", 32),
			"PROMETHEUS_URL": "http://prometheus:9090", "TARGETS_FILE": "unused",
			"PUBLIC_URL": "http://monitor.test", "ALLOWED_TARGET_CIDRS": "127.0.0.0/8,10.0.0.0/24",
			"ALLOWED_TARGET_PORTS": fmt.Sprintf("9100,%d", expPort),
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	// Tests talk to exporters on loopback, which production always forbids.
	var denied []netip.Prefix
	for _, p := range cfg.DeniedTargetCIDRs {
		if !p.Contains(netip.MustParseAddr("127.0.0.1")) {
			denied = append(denied, p)
		}
	}
	cfg.DeniedTargetCIDRs = denied

	fp := &fakeProm{}
	promSrv := httptest.NewServer(fp)
	t.Cleanup(promSrv.Close)
	tw := targets.NewWriter(pool, filepath.Join(t.TempDir(), "fleet.json"), log)
	svc := auth.NewService(pool, cfg.CSRFSecret, time.Hour, 24*time.Hour)
	api := httpapi.New(httpapi.Deps{Config: cfg, Pool: pool, Auth: svc, Prom: metrics.NewClient(promSrv.URL), Targets: tw, Log: log})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, pool: pool, prom: fp, targets: tw, cfg: cfg, expPort: expPort, promURL: promSrv.URL}
}

// client is a browser-like session: cookie jar plus the CSRF token after login.
type client struct {
	e    *env
	http *http.Client
	csrf string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

type apiResp struct {
	status int
	body   map[string]any
	raw    []byte
	header http.Header
}

func (c *client) do(method, path, body string) apiResp {
	return c.doHdr(method, path, body, nil)
}

func (c *client) doHdr(method, path, body string, hdr map[string]string) apiResp {
	c.e.t.Helper()
	req, _ := http.NewRequest(method, c.e.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := apiResp{status: resp.StatusCode, header: resp.Header}
	out.raw, _ = io.ReadAll(resp.Body)
	if len(out.raw) > 0 {
		if err := json.Unmarshal(out.raw, &out.body); err != nil {
			c.e.t.Fatalf("%s %s: non-JSON body %q", method, path, out.raw)
		}
	}
	return out
}

func (c *client) login(email, pass string) apiResp {
	r := c.do("POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"`+pass+`"}`)
	if tok, ok := r.body["csrf_token"].(string); ok {
		c.csrf = tok
	}
	return r
}

// as returns a signed-in client with the given role (admin uses the bootstrap account).
func (e *env) as(role string) *client {
	e.t.Helper()
	c := e.client()
	if role == "admin" {
		if r := c.login("admin@example.com", adminPass); r.status != 200 {
			e.t.Fatalf("admin login: %d %v", r.status, r.body)
		}
		return c
	}
	email := role + "@example.com"
	hash, _ := auth.HashPassword(role + "-password-1")
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO users (email, name, password_hash, role) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		email, role, hash, role); err != nil {
		e.t.Fatal(err)
	}
	if r := c.login(email, role+"-password-1"); r.status != 200 {
		e.t.Fatalf("%s login: %d %v", role, r.status, r.body)
	}
	return c
}

func errCode(r apiResp) string {
	if e, ok := r.body["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func field(r apiResp, name string) string {
	if e, ok := r.body["error"].(map[string]any); ok {
		if f, ok := e["fields"].(map[string]any); ok {
			s, _ := f[name].(string)
			return s
		}
	}
	return ""
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}
