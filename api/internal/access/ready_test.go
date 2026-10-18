package access

import (
	"context"
	"strings"
	"testing"
)

// A server listed with ready: false (setup not done) is never contacted and
// cannot be requested; existing grants simply do not reach it.
func TestNotReadyServerIsNeverContacted(t *testing.T) {
	p, err := ParsePolicy([]byte(`
servers:
  bm3: {host: 10.0.0.13, ssh_user: root, ready: false}
  demo: {host: 172.31.50.10, ssh_user: root, login_host: localhost, login_port: 2222}
max_days: 30
max_sudo_days: 7
admins:
  - {github: alice-gh, email: alice@example.com}
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Servers["bm3"].IsReady() || !p.Servers["demo"].IsReady() {
		t.Fatal("ready flags not read")
	}
	if probs := p.ValidateGrant(grant(t, "0080", "rakib@example.com", "bm3"), now, true); len(probs) == 0 || !strings.Contains(probs.Error(), "not ready") {
		t.Errorf("request for a not-ready server: %v", probs)
	}
	g := grant(t, "0081", "rakib@example.com", "bm3", "demo")
	remote := &fakeRemote{applied: map[string][]Account{}, fail: map[string]bool{}}
	res, err := Apply(context.Background(), ApplyInput{File: &File{Grants: []Grant{g}}, Policy: p, CA: newCA(t),
		Verifier: fakeVerifier{"0081": 1}, Remote: remote, KRL: fakeKRL, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, contacted := remote.applied["bm3"]; contacted {
		t.Fatal("the runner connected to a server that is not ready")
	}
	if users(remote.applied["demo"]) != "rakib" || !strings.Contains(res.Status.Servers["bm3"].Error, "not ready") {
		t.Fatalf("demo=%v bm3=%+v", remote.applied["demo"], res.Status.Servers["bm3"])
	}
}
