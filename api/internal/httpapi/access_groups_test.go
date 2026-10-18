package httpapi_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bm-monitor/api/internal/access"
)

// SSH access by group: a request names an access group from the policy, and a
// dashboard group is synced to its access group through a policy pull request.
func TestAccessByGroup(t *testing.T) {
	e := newAccessEnv(t)
	ctx := context.Background()
	admin := e.as("admin")

	// The policy has no access groups yet: nothing to request by group.
	if r := admin.do("GET", "/api/v1/access/config", ""); len(r.body["groups"].([]any)) != 0 {
		t.Fatalf("groups before = %s", r.raw)
	}

	// A dashboard group puku-bot with bm3 (known to the policy) and bm7 (not set up yet).
	r := admin.do("POST", "/api/v1/servers", `{"name":"bm3","exporter_url":"http://10.0.0.13:9100/metrics","force":true}`)
	bm3 := dig(r.body, "server", "id").(string)
	r = admin.do("POST", "/api/v1/servers", `{"name":"bm7","exporter_url":"http://10.0.0.17:9100/metrics","force":true}`)
	bm7 := dig(r.body, "server", "id").(string)
	r = admin.do("POST", "/api/v1/groups", fmt.Sprintf(`{"name":"puku-bot","server_ids":[%q,%q]}`, bm3, bm7))
	group := r.body["id"].(string)

	ga := admin.do("GET", "/api/v1/groups/"+group+"/access", "")
	if ga.body["in_policy"] != false || fmt.Sprint(ga.body["wanted"]) != "[bm3]" || fmt.Sprint(ga.body["not_ready"]) != "[bm7]" {
		t.Fatalf("access before = %s", ga.raw)
	}
	// Only admins propose.
	if r := e.as("operator").do("POST", "/api/v1/groups/"+group+"/access/propose", ""); r.status != 403 {
		t.Errorf("operator propose = %d", r.status)
	}
	r = admin.do("POST", "/api/v1/groups/"+group+"/access/propose", "")
	if r.status != 201 || r.body["status"] != "pr_open" {
		t.Fatalf("propose = %d %s", r.status, r.raw)
	}
	pr := e.gh.prs[len(e.gh.prs)-1]
	newPolicy := e.gh.branches[pr.Head][access.PolicyPath]
	if !strings.Contains(newPolicy, "puku-bot: [bm3]") || pr.Labels[0] != "access-policy" ||
		!strings.Contains(pr.Body, "Requested by: admin@example.com") || !strings.Contains(pr.Body, "bm7: not in the policy") {
		t.Fatalf("policy PR:\n%s\n---\n%s", pr.Body, newPolicy)
	}
	if r := admin.do("POST", "/api/v1/groups/"+group+"/access/propose", ""); r.status != 409 {
		t.Errorf("second open proposal = %d", r.status)
	}
	e.gh.merge(pr.Number)
	if err := e.svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	ga = admin.do("GET", "/api/v1/groups/"+group+"/access", "")
	if ga.body["in_sync"] != true || dig(ga.body, "proposal", "status") != "merged" {
		t.Fatalf("access after merge = %s", ga.raw)
	}

	// Now a viewer can request the group; sudo is refused for groups.
	body := `{"groups":["puku-bot"],"days":5,"reason":"Debug the bot workers","public_key":"` + pubKey(t) + `"}`
	if r := admin.do("GET", "/api/v1/access/config", ""); len(r.body["groups"].([]any)) != 1 {
		t.Fatalf("config groups = %s", r.raw)
	}
	viewer := e.as("viewer")
	if r := viewer.do("POST", "/api/v1/access/requests", `{"groups":["puku-bot"],"sudo":true,"days":2,"reason":"Debug the bot workers","public_key":"`+pubKey(t)+`"}`); r.status != 400 || field(r, "sudo") == "" {
		t.Errorf("sudo on a group = %d %s", r.status, r.raw)
	}
	if r := viewer.do("POST", "/api/v1/access/requests", `{"groups":["nope"],"days":2,"reason":"Debug the bot workers","public_key":"`+pubKey(t)+`"}`); r.status != 400 || field(r, "groups") == "" {
		t.Errorf("unknown group = %d %s", r.status, r.raw)
	}
	r = viewer.do("POST", "/api/v1/access/requests", body)
	if r.status != 201 || fmt.Sprint(r.body["groups"]) != "[puku-bot]" {
		t.Fatalf("group request = %d %s", r.status, r.raw)
	}
	req := e.gh.prs[len(e.gh.prs)-1]
	if !strings.Contains(req.Title, "group puku-bot") || !strings.Contains(req.Body, "| Covers today | bm3 |") {
		t.Errorf("request PR: %s\n%s", req.Title, req.Body)
	}
	head, _ := access.ParseFile([]byte(e.gh.branches[req.Head][access.GrantsPath]))
	if g := head.Grants[len(head.Grants)-1]; len(g.Groups) != 1 || len(g.Servers) != 0 {
		t.Fatalf("grant = %+v", g)
	}

	// The grants list shows what each grant covers today.
	e.gh.approve(req.Number, "admin-gh")
	e.gh.merge(req.Number)
	g := admin.do("GET", "/api/v1/access/grants", "")
	item := g.body["items"].([]any)[0].(map[string]any)
	if fmt.Sprint(item["covers"]) != "[bm3]" || fmt.Sprint(item["groups"]) != "[puku-bot]" {
		t.Fatalf("grants = %s", g.raw)
	}
}
