package httpapi_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"bm-monitor/api/internal/access"
	"bm-monitor/api/internal/accessreq"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/config"
	"bm-monitor/api/internal/db"
	"bm-monitor/api/internal/dbtest"
	"bm-monitor/api/internal/ghclient"
	"bm-monitor/api/internal/httpapi"
	"bm-monitor/api/internal/metrics"
)

// fakeGitHub keeps branches as maps of path → content and records pull requests.
type fakeGitHub struct {
	mu       sync.Mutex
	branches map[string]map[string]string // branch → path → content
	prs      []*fakePR
}

type fakePR struct {
	Number       int
	Title, Body  string
	Head, Base   string
	Labels       []string
	State        string
	Merged       bool
	HeadSnapshot map[string]string
	Reviews      []access.Review
}

func newFakeGitHub(policy string) *fakeGitHub {
	return &fakeGitHub{branches: map[string]map[string]string{
		"main": {access.PolicyPath: policy, access.GrantsPath: "grants: []\n"},
	}}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/repos/o/r")
	send := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == "GET" && strings.HasPrefix(path, "/git/ref/heads/"):
		b := strings.TrimPrefix(path, "/git/ref/heads/")
		if _, ok := f.branches[b]; !ok {
			send(404, map[string]string{"message": "Not Found"})
			return
		}
		send(200, map[string]any{"object": map[string]string{"sha": "sha-" + b}})
	case r.Method == "GET" && strings.HasPrefix(path, "/contents/"):
		ref := strings.TrimPrefix(r.URL.Query().Get("ref"), "sha-")
		c, ok := f.branches[ref][strings.TrimPrefix(path, "/contents/")]
		if !ok {
			send(404, map[string]string{"message": "Not Found"})
			return
		}
		send(200, map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(c)), "sha": "blob"})
	case r.Method == "POST" && path == "/git/refs":
		name := strings.TrimPrefix(body["ref"].(string), "refs/heads/")
		src := f.branches[strings.TrimPrefix(body["sha"].(string), "sha-")]
		cp := map[string]string{}
		for k, v := range src {
			cp[k] = v
		}
		f.branches[name] = cp
		send(201, map[string]any{})
	case r.Method == "PUT" && strings.HasPrefix(path, "/contents/"):
		c, _ := base64.StdEncoding.DecodeString(body["content"].(string))
		f.branches[body["branch"].(string)][strings.TrimPrefix(path, "/contents/")] = string(c)
		send(200, map[string]any{})
	case r.Method == "POST" && path == "/pulls":
		pr := &fakePR{Number: len(f.prs) + 1, Title: body["title"].(string), Body: body["body"].(string),
			Head: body["head"].(string), Base: body["base"].(string), State: "open"}
		f.prs = append(f.prs, pr)
		send(201, map[string]any{"number": pr.Number, "html_url": fmt.Sprintf("https://github.com/o/r/pull/%d", pr.Number), "state": "open"})
	case r.Method == "POST" && strings.HasSuffix(path, "/labels"):
		var n int
		fmt.Sscanf(path, "/issues/%d/labels", &n)
		for _, l := range body["labels"].([]any) {
			f.prs[n-1].Labels = append(f.prs[n-1].Labels, l.(string))
		}
		send(200, []any{})
	case r.Method == "GET" && strings.HasSuffix(path, "/reviews"):
		var n int
		fmt.Sscanf(path, "/pulls/%d/reviews", &n)
		var out []map[string]any
		for _, rv := range f.prs[n-1].Reviews {
			out = append(out, map[string]any{"user": map[string]string{"login": rv.User}, "state": rv.State, "commit_id": rv.CommitID})
		}
		send(200, out)
	case r.Method == "GET" && strings.HasPrefix(path, "/pulls/"):
		var n int
		fmt.Sscanf(path, "/pulls/%d", &n)
		pr := f.prs[n-1]
		var mergedAt any
		if pr.Merged {
			mergedAt = "2026-10-17T10:00:00Z"
		}
		send(200, map[string]any{"number": n, "state": pr.State, "merged_at": mergedAt, "html_url": "x",
			"head": map[string]string{"sha": "sha-" + pr.Head, "ref": pr.Head}, "base": map[string]string{"ref": pr.Base}})
	default:
		send(404, map[string]string{"message": "unhandled " + r.Method + " " + path})
	}
}

// approve records an approving review by login on pull request n's head commit.
func (f *fakeGitHub) approve(n int, login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.prs[n-1]
	pr.Reviews = append(pr.Reviews, access.Review{User: login, State: "APPROVED", CommitID: "sha-" + pr.Head})
}

// merge simulates an admin merging pull request n into main.
func (f *fakeGitHub) merge(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.prs[n-1]
	pr.State, pr.Merged = "closed", true
	f.branches["main"][access.GrantsPath] = f.branches[pr.Head][access.GrantsPath]
}

// publishState simulates the apply job writing status.json and certificates.
func (f *fakeGitHub) publishState(st access.Status, certs map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(st)
	m := map[string]string{"status.json": string(b)}
	for id, c := range certs {
		m["certs/"+id+"-cert.pub"] = c
	}
	f.branches["access-state"] = m
}

const testPolicy = `servers:
  bm3: {host: 10.0.0.13, ssh_user: bm-access-deploy, production: false}
  bm1: {host: 10.0.0.11, ssh_user: bm-access-deploy, production: true}
max_days: 30
max_sudo_days: 7
admins:
  - {github: admin-gh, email: admin@example.com}
  - {github: ops-gh, email: ops@example.com}
`

type accessEnv struct {
	*env
	gh  *fakeGitHub
	svc *accessreq.Service
}

func newAccessEnv(t *testing.T) *accessEnv {
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
	cfg, err := config.LoadFrom(func(k string) string {
		return map[string]string{"DATABASE_URL": "x", "SESSION_SECRET": strings.Repeat("s", 32), "CSRF_SECRET": strings.Repeat("c", 32),
			"PROMETHEUS_URL": "http://p:9090", "TARGETS_FILE": "x", "PUBLIC_URL": "http://monitor.test", "ALLOWED_TARGET_CIDRS": "10.0.0.0/8"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := newFakeGitHub(testPolicy)
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)
	svc := accessreq.New(pool, ghclient.New(ghSrv.URL, "o/r", ghclient.StaticToken("t")), log)
	promSrv := httptest.NewServer(&fakeProm{})
	t.Cleanup(promSrv.Close)
	api := httpapi.New(httpapi.Deps{Config: cfg, Pool: pool, Auth: auth.NewService(pool, cfg.CSRFSecret, time.Hour, 24*time.Hour),
		Prom: metrics.NewClient(promSrv.URL), Log: log, Access: svc})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &accessEnv{env: &env{t: t, srv: srv, pool: pool}, gh: gh, svc: svc}
}

func pubKey(t *testing.T) string {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " laptop"
}

func TestAccessRequestOpensPullRequestAndTracksIt(t *testing.T) {
	e := newAccessEnv(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("rakib-password-1")
	e.pool.Exec(ctx, `INSERT INTO users (email, name, password_hash, role) VALUES ('rakib@example.com','Rakib',$1,'viewer')`, hash)
	rakib := e.client()
	if r := rakib.login("rakib@example.com", "rakib-password-1"); r.status != 200 {
		t.Fatalf("login %d", r.status)
	}

	r := rakib.do("GET", "/api/v1/access/config", "")
	if r.body["enabled"] != true || r.body["unix_user"] != "rakib" || len(r.body["servers"].([]any)) != 2 {
		t.Fatalf("config = %s", r.raw)
	}

	// Validation uses the same rules as CI.
	for body, want := range map[string]string{
		`{"servers":["bm3"],"days":40,"reason":"Debug the sandbox","public_key":"` + pubKey(t) + `"}`:                  "days",
		`{"servers":["bm3"],"sudo":true,"days":10,"reason":"Debug the sandbox","public_key":"` + pubKey(t) + `"}`:      "days",
		`{"servers":["bm9"],"days":5,"reason":"Debug the sandbox","public_key":"` + pubKey(t) + `"}`:                   "servers",
		`{"servers":["bm3"],"days":5,"reason":"Debug the sandbox","public_key":"-----BEGIN OPENSSH PRIVATE KEY-----"}`: "public_key",
		`{"servers":["bm3"],"days":5,"reason":"fix","public_key":"` + pubKey(t) + `"}`:                                 "reason",
	} {
		if r := rakib.do("POST", "/api/v1/access/requests", body); r.status != 400 || field(r, want) == "" {
			t.Errorf("%s → %d %s, want error on %s", body[:40], r.status, r.raw, want)
		}
	}

	key := pubKey(t)
	r = rakib.do("POST", "/api/v1/access/requests", `{"servers":["bm3"],"days":14,"reason":"Debug the pandastack sandbox","public_key":"`+key+`"}`)
	if r.status != 201 || r.body["status"] != "pr_open" || r.body["grant_id"] != "0001" {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}

	// The pull request adds exactly one entry to the shared file and is labelled.
	if len(e.gh.prs) != 1 {
		t.Fatalf("prs = %d", len(e.gh.prs))
	}
	pr := e.gh.prs[0]
	if pr.Head != "access/request-0001" || pr.Base != "main" || len(pr.Labels) != 1 || pr.Labels[0] != "access-request" {
		t.Errorf("pr = %+v", pr)
	}
	if !strings.Contains(pr.Body, "Approvals required: 1") || !strings.Contains(pr.Title, "rakib → bm3") {
		t.Errorf("pr text: %s / %s", pr.Title, pr.Body)
	}
	head, _ := access.ParseFile([]byte(e.gh.branches["access/request-0001"][access.GrantsPath]))
	base, _ := access.ParseFile([]byte(e.gh.branches["main"][access.GrantsPath]))
	p, _ := access.ParsePolicy([]byte(testPolicy))
	if chk := p.CheckPR(base, head, access.KindRequest, time.Now()); len(chk.Problems) > 0 {
		t.Fatalf("the dashboard's own pull request fails CI rules: %v", chk.Problems)
	}

	// One open request at a time.
	if r := rakib.do("POST", "/api/v1/access/requests", `{"servers":["bm3"],"days":1,"reason":"Second request here","public_key":"`+pubKey(t)+`"}`); r.status != 409 {
		t.Errorf("second open request = %d", r.status)
	}

	// Approve + merge + apply job publishes status and certificate → request becomes active.
	e.gh.approve(1, "admin-gh")
	e.gh.merge(1)
	if err := e.svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	r = rakib.do("GET", "/api/v1/access/requests", "")
	if st := r.body["items"].([]any)[0].(map[string]any)["status"]; st != "merged" {
		t.Fatalf("after merge: %v", st)
	}
	e.gh.publishState(access.Status{GeneratedAt: time.Now().Add(time.Minute), Grants: map[string]access.GrantStatus{
		"0001": {Status: "active", Servers: map[string]string{"bm3": "ok"}}}}, map[string]string{"0001": "ssh-ed25519-cert-v01@openssh.com AAAAfake rakib"})
	if err := e.svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	item := rakib.do("GET", "/api/v1/access/requests", "").body["items"].([]any)[0].(map[string]any)
	if item["status"] != "active" || !strings.HasPrefix(fmt.Sprint(item["certificate"]), "ssh-ed25519-cert") {
		t.Fatalf("after apply: %v", item)
	}

	// Only admins see all grants, and they do not get other people's certificates.
	if r := rakib.do("GET", "/api/v1/access/grants", ""); r.status != 403 {
		t.Errorf("viewer listing grants = %d", r.status)
	}
	if r := rakib.do("GET", "/api/v1/access/requests?all=1", ""); r.status != 403 {
		t.Errorf("viewer listing all requests = %d", r.status)
	}
	admin := e.as("admin")
	all := admin.do("GET", "/api/v1/access/requests?all=1", "").body["items"].([]any)
	if all[0].(map[string]any)["certificate"] != nil {
		t.Error("admin sees someone else's certificate")
	}
	g := admin.do("GET", "/api/v1/access/grants", "")
	if items := g.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["status"] != "active" {
		t.Fatalf("grants = %s", g.raw)
	}

	// Revoke: a pull request that removes exactly that entry.
	if r := admin.do("POST", "/api/v1/access/grants/0001/revoke", ""); r.status != 201 {
		t.Fatalf("revoke = %d %s", r.status, r.raw)
	}
	rv := e.gh.prs[1]
	after, _ := access.ParseFile([]byte(e.gh.branches[rv.Head][access.GrantsPath]))
	before, _ := access.ParseFile([]byte(e.gh.branches["main"][access.GrantsPath]))
	if len(rv.Labels) != 1 || rv.Labels[0] != "access-revoke" || len(after.Grants) != 0 {
		t.Fatalf("revoke pr = %+v, grants after = %d", rv, len(after.Grants))
	}
	if chk := p.CheckPR(before, after, access.KindRevoke, time.Now()); len(chk.Problems) > 0 {
		t.Fatalf("revoke PR fails CI rules: %v", chk.Problems)
	}
	e.gh.merge(2)
	e.gh.publishState(access.Status{GeneratedAt: time.Now().Add(2 * time.Minute), Grants: map[string]access.GrantStatus{}}, nil)
	if err := e.svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, it := range rakib.do("GET", "/api/v1/access/requests", "").body["items"].([]any) {
		m := it.(map[string]any)
		if m["status"] != "revoked" {
			t.Errorf("%s %s after revoke = %v (%v)", m["kind"], m["grant_id"], m["status"], m["status_detail"])
		}
	}

	// Audit trail.
	var n int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action IN ('access.request','access.revoke_requested')`).Scan(&n)
	if n != 2 {
		t.Errorf("audit events = %d, want 2", n)
	}
}

// GitHub's free plan lets an admin merge without approvals. The dashboard must
// not call that approved, and must not block the requester forever.
func TestAccessMergedWithoutApprovalsIsNotApplied(t *testing.T) {
	e := newAccessEnv(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("ops-password-123")
	e.pool.Exec(ctx, `INSERT INTO users (email, name, password_hash, role) VALUES ('ops@example.com','Ops',$1,'admin')`, hash)
	admin := e.client()
	if r := admin.login("ops@example.com", "ops-password-123"); r.status != 200 {
		t.Fatalf("login %d", r.status)
	}
	r := admin.do("POST", "/api/v1/access/requests", `{"servers":["bm3"],"days":3,"reason":"Deploy a feature to bm3","public_key":"`+pubKey(t)+`"}`)
	if r.status != 201 {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	e.gh.approve(1, "ops-gh") // the requester's own approval
	e.gh.merge(1)
	if err := e.svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	item := admin.do("GET", "/api/v1/access/requests", "").body["items"].([]any)[0].(map[string]any)
	detail := fmt.Sprint(item["status_detail"])
	if item["status"] != "failed" || !strings.Contains(detail, "(0 of 1)") || !strings.Contains(detail, "cannot approve their own request") {
		t.Fatalf("after self-approved merge: %v", item)
	}
	if r := admin.do("POST", "/api/v1/access/requests", `{"servers":["bm3"],"days":3,"reason":"Deploy a feature to bm3","public_key":"`+pubKey(t)+`"}`); r.status != 201 {
		t.Errorf("new request after a failed one = %d %s", r.status, r.raw)
	}

	// Revoking it needs one admin approval, and the grant owner's own counts.
	p, _ := access.ParsePolicy([]byte(testPolicy))
	g := access.Grant{ID: "0001", Email: "ops@example.com"}
	if chk := p.CheckPR(&access.File{Grants: []access.Grant{g}}, &access.File{}, access.KindRevoke, time.Now()); chk.RequesterEmail != "" {
		t.Errorf("revoke requester = %q, want none", chk.RequesterEmail)
	}
	if n, _ := p.CountApprovals([]access.Review{{User: "ops-gh", State: "APPROVED", CommitID: "h"}}, "h", ""); n != 1 {
		t.Errorf("owner approving their own revoke counts %d", n)
	}
}

func TestAccessNotConfigured(t *testing.T) {
	e := newEnv(t)
	c := e.as("viewer")
	if r := c.do("GET", "/api/v1/access/config", ""); r.status != 200 || r.body["enabled"] != false {
		t.Fatalf("config = %d %s", r.status, r.raw)
	}
	if r := c.do("POST", "/api/v1/access/requests", `{"servers":["bm3"],"days":1,"reason":"xxxxxxxxxxxx","public_key":"x"}`); r.status != 503 {
		t.Errorf("request without config = %d", r.status)
	}
}
