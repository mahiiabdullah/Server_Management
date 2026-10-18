package httpapi_test

import (
	"fmt"
	"strings"
	"testing"
)

func (e *env) group(c *client, name string) string {
	e.t.Helper()
	r := c.do("POST", "/api/v1/groups", fmt.Sprintf(`{"name":%q,"description":"test"}`, name))
	if r.status != 201 {
		e.t.Fatalf("create group %s = %d %s", name, r.status, r.raw)
	}
	return r.body["id"].(string)
}

func groupNames(server any) []string {
	var out []string
	for _, g := range server.(map[string]any)["groups"].([]any) {
		out = append(out, g.(map[string]any)["name"].(string))
	}
	return out
}

// One server can serve several groups, and a group can have several servers.
func TestServerInSeveralGroups(t *testing.T) {
	e := newEnv(t)
	op := e.as("operator")
	chat, bot := e.group(op, "puku-web-chat"), e.group(op, "puku-bot")

	// Create with two groups; they come back sorted by name.
	r := op.do("POST", "/api/v1/servers", fmt.Sprintf(`{"name":"bm1","exporter_url":%q,"group_ids":[%q,%q,%q]}`, e.exporterURL(), chat, bot, chat))
	if r.status != 201 {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	id := dig(r.body, "server", "id").(string)
	if g := groupNames(r.body["server"]); strings.Join(g, ",") != "puku-bot,puku-web-chat" {
		t.Fatalf("groups = %v", g)
	}
	labels := e.targetsFile()[0]["labels"].(map[string]any)
	if labels["groups"] != ",puku-bot,puku-web-chat," {
		t.Errorf("prometheus groups label = %v", labels["groups"])
	}

	// Unknown or malformed group ids are refused.
	for _, body := range []string{`{"group_ids":["00000000-0000-0000-0000-000000000000"]}`, `{"group_ids":["nope"]}`} {
		if r := op.do("PATCH", "/api/v1/servers/"+id, body); r.status != 400 || field(r, "group_ids") == "" {
			t.Errorf("%s → %d %s", body, r.status, r.raw)
		}
	}

	// Filter the list by either group.
	for _, g := range []string{chat, bot} {
		if r := op.do("GET", "/api/v1/servers?group="+g, ""); r.body["total"] != float64(1) {
			t.Errorf("filter %s = %s", g, r.raw)
		}
	}

	// A second server only in chat, added from the group side.
	r = e.as("admin").do("POST", "/api/v1/servers", `{"name":"bm2","exporter_url":"http://10.0.0.2:9100/metrics","force":true}`)
	if r.status != 201 {
		t.Fatalf("bm2 = %d %s", r.status, r.raw)
	}
	other := dig(r.body, "server", "id").(string)
	if r := op.do("PUT", "/api/v1/groups/"+chat+"/servers", fmt.Sprintf(`{"server_ids":[%q,%q]}`, id, other)); r.status != 200 || r.body["server_count"] != float64(2) {
		t.Fatalf("set servers = %d %s", r.status, r.raw)
	}

	// Summary and detail.
	sum := op.do("GET", "/api/v1/groups?summary=1", "")
	byName := map[string]map[string]any{}
	for _, it := range sum.body["items"].([]any) {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if c := byName["puku-web-chat"]; c["server_count"] != float64(2) || c["shared_servers"] != float64(1) {
		t.Errorf("chat summary = %v", c)
	}
	if b := byName["puku-bot"]; b["server_count"] != float64(1) {
		t.Errorf("bot summary = %v", b)
	}
	d := op.do("GET", "/api/v1/groups/"+chat, "")
	if d.status != 200 || len(d.body["servers"].([]any)) != 2 || dig(d.body, "group", "name") != "puku-web-chat" {
		t.Fatalf("detail = %d %s", d.status, d.raw)
	}

	// Removing a group from the server leaves the other one.
	if r := op.do("PATCH", "/api/v1/servers/"+id, fmt.Sprintf(`{"group_ids":[%q]}`, bot)); r.status != 200 || strings.Join(groupNames(r.body["server"]), ",") != "puku-bot" {
		t.Fatalf("patch groups = %d %s", r.status, r.raw)
	}
	if r := op.do("PATCH", "/api/v1/servers/"+id, `{"group_ids":[]}`); r.status != 200 || len(groupNames(r.body["server"])) != 0 {
		t.Fatalf("clear groups = %d %s", r.status, r.raw)
	}
	if r := op.do("GET", "/api/v1/servers?group=none", ""); r.body["total"] != float64(1) {
		t.Errorf("servers without a group = %s", r.raw)
	}

	// Deleting a group removes only the membership.
	if r := e.as("admin").do("DELETE", "/api/v1/groups/"+chat, ""); r.status != 204 {
		t.Fatalf("delete group = %d %s", r.status, r.raw)
	}
	if r := op.do("GET", "/api/v1/servers/"+other, ""); r.status != 200 || len(groupNames(dig(r.body, "server"))) != 0 {
		t.Errorf("server after group delete = %d %s", r.status, r.raw)
	}

	// Viewers can read groups but not change them.
	v := e.as("viewer")
	if r := v.do("GET", "/api/v1/groups/"+bot, ""); r.status != 200 {
		t.Errorf("viewer read = %d", r.status)
	}
	if r := v.do("PUT", "/api/v1/groups/"+bot+"/servers", `{"server_ids":[]}`); r.status != 403 {
		t.Errorf("viewer write = %d", r.status)
	}
}
