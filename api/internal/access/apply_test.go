package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type fakeVerifier map[string]int // grant id → admin approvals of its merged pull request (0 = none)

func (f fakeVerifier) Verify(_ context.Context, g Grant) (bool, int, string, error) {
	if n := f[g.ID]; n > 0 {
		return true, n, "", nil
	}
	return false, 0, "no approved pull request", nil
}

type fakeRemote struct {
	applied map[string][]Account
	fail    map[string]bool
}

func (r *fakeRemote) Apply(_ context.Context, name string, _ Server, acc []Account, _ []byte) error {
	if r.fail[name] {
		return errors.New("ssh: connect timeout")
	}
	r.applied[name] = acc
	return nil
}

func fakeKRL(_ ssh.PublicKey, serials []uint64) ([]byte, error) {
	return []byte(fmt.Sprint(serials)), nil
}

func newCA(t *testing.T) ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := ssh.NewSignerFromKey(priv)
	return s
}

func TestApplyOnlyApprovedGrantsAndRevocation(t *testing.T) {
	p := policy()
	ca := newCA(t)
	a := grant(t, "0041", "alice@example.com", "bm1", "bm3")
	r := grant(t, "0042", "rakib@example.com", "bm3")
	sneaky := grant(t, "0043", "eve@example.com", "bm1") // pushed without approval
	remote := &fakeRemote{applied: map[string][]Account{}, fail: map[string]bool{}}
	in := ApplyInput{File: &File{Grants: []Grant{a, r, sneaky}}, Policy: p, CA: ca,
		Verifier: fakeVerifier{"0041": 2, "0042": 1}, Remote: remote, KRL: fakeKRL, Now: now, Commit: "abc"}

	res, err := Apply(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Grants["0043"].Status != "awaiting_approval" || res.Certs["0043"] != nil {
		t.Fatalf("unapproved grant was applied: %+v", res.Status.Grants["0043"])
	}
	for _, acc := range remote.applied["bm1"] {
		if acc.User == "eve" {
			t.Fatal("unapproved user created on bm1")
		}
	}
	if len(remote.applied["bm3"]) != 2 || len(res.Certs) != 2 || string(res.KRL) != "[]" {
		t.Fatalf("bm3=%v certs=%d krl=%s", remote.applied["bm3"], len(res.Certs), res.KRL)
	}
	if res.Status.Grants["0042"].Servers["bm3"] != "ok" {
		t.Errorf("grant status: %+v", res.Status.Grants["0042"])
	}

	// Second run: rakib's grant revoked (removed from the file) → its serial is
	// revoked, the account is gone, and approval is not asked again for alice.
	in2 := in
	in2.File = &File{Grants: []Grant{a}}
	in2.State = res.State
	in2.Certs = res.Certs
	in2.Verifier = fakeVerifier{} // nothing new to verify: alice is cached
	res2, err := Apply(context.Background(), in2)
	if err != nil {
		t.Fatal(err)
	}
	if string(res2.KRL) != "[42]" {
		t.Errorf("revocation list = %s, want [42]", res2.KRL)
	}
	if len(remote.applied["bm3"]) != 1 || remote.applied["bm3"][0].User != "alice" {
		t.Errorf("bm3 after revoke: %v", remote.applied["bm3"])
	}
	if res2.Status.Grants["0041"].Status != "active" {
		t.Errorf("alice lost access: %+v", res2.Status.Grants["0041"])
	}
	if string(res2.Certs["0041"]) != string(res.Certs["0041"]) {
		t.Error("an unchanged certificate was re-signed")
	}

	// After rakib's certificate would have expired, it drops out of the list.
	in3 := in2
	in3.State, in3.Certs, in3.Now = res2.State, res2.Certs, r.Expires.Add(time.Minute)
	res3, _ := Apply(context.Background(), in3)
	if strings.Contains(string(res3.KRL), "42") {
		t.Errorf("expired serial still revoked: %s", res3.KRL)
	}
}

func TestApplyContinuesWhenOneServerFails(t *testing.T) {
	p := policy()
	a := grant(t, "0041", "alice@example.com", "bm1", "bm3")
	remote := &fakeRemote{applied: map[string][]Account{}, fail: map[string]bool{"bm1": true}}
	res, err := Apply(context.Background(), ApplyInput{File: &File{Grants: []Grant{a}}, Policy: p, CA: newCA(t),
		Verifier: fakeVerifier{"0041": 2}, Remote: remote, KRL: fakeKRL, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Servers["bm1"].OK || !res.Status.Servers["bm3"].OK {
		t.Fatalf("servers: %+v", res.Status.Servers)
	}
	gs := res.Status.Grants["0041"]
	if gs.Servers["bm3"] != "ok" || !strings.Contains(gs.Servers["bm1"], "timeout") {
		t.Errorf("per-server grant status: %+v", gs.Servers)
	}
}

func TestApplyDetectsTamperedApprovedGrant(t *testing.T) {
	p := policy()
	a := grant(t, "0041", "alice@example.com", "bm3")
	remote := &fakeRemote{applied: map[string][]Account{}, fail: map[string]bool{}}
	in := ApplyInput{File: &File{Grants: []Grant{a}}, Policy: p, CA: newCA(t), Verifier: fakeVerifier{"0041": 1},
		Remote: remote, KRL: fakeKRL, Now: now}
	res, _ := Apply(context.Background(), in)

	// Someone edits the approved grant directly on main to add sudo.
	tampered := a
	tampered.Sudo = true
	in.File, in.State, in.Certs = &File{Grants: []Grant{tampered}}, res.State, res.Certs
	in.Verifier = fakeVerifier{} // the edit has no approved pull request
	res2, _ := Apply(context.Background(), in)
	if res2.Status.Grants["0041"].Status != "awaiting_approval" {
		t.Fatalf("tampered grant still active: %+v", res2.Status.Grants["0041"])
	}
	if len(remote.applied["bm3"]) != 0 {
		t.Errorf("tampered grant applied: %v", remote.applied["bm3"])
	}
	if string(res2.KRL) != "[41]" {
		t.Errorf("certificate of the tampered grant not revoked: %s", res2.KRL)
	}
}

func groupPolicy(t *testing.T, groups string) *Policy {
	t.Helper()
	p, err := ParsePolicy([]byte(`
servers:
  bm1: {host: 10.0.0.11, ssh_user: root, production: true}
  bm3: {host: 10.0.0.13, ssh_user: root, production: false}
  bm4: {host: 10.0.0.14, ssh_user: root, production: false}
max_days: 30
max_sudo_days: 7
admins:
  - {github: alice-gh, email: alice@example.com}
groups:
` + groups))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func users(acc []Account) string {
	var u []string
	for _, a := range acc {
		u = append(u, a.User)
	}
	return strings.Join(u, ",")
}

// A group grant follows its group: servers added later are covered, removed ones
// lose the account, and a production server joining needs two approvals.
func TestGroupGrantFollowsTheGroup(t *testing.T) {
	g := grant(t, "0050", "rakib@example.com")
	g.Groups = []string{"puku-bot"}
	remote := &fakeRemote{applied: map[string][]Account{}, fail: map[string]bool{}}
	in := ApplyInput{File: &File{Grants: []Grant{g}}, Policy: groupPolicy(t, "  puku-bot: [bm3]\n"), CA: newCA(t),
		Verifier: fakeVerifier{"0050": 1}, Remote: remote, KRL: fakeKRL, Now: now}
	res, err := Apply(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if users(remote.applied["bm3"]) != "rakib" || users(remote.applied["bm4"]) != "" {
		t.Fatalf("first run: bm3=%v bm4=%v", remote.applied["bm3"], remote.applied["bm4"])
	}

	// bm4 and the production bm1 join the group (a reviewed policy change).
	in.Policy, in.State, in.Certs, in.Verifier = groupPolicy(t, "  puku-bot: [bm1, bm3, bm4]\n"), res.State, res.Certs, fakeVerifier{}
	res, err = Apply(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if users(remote.applied["bm4"]) != "rakib" || users(remote.applied["bm1"]) != "" {
		t.Fatalf("after growth: bm1=%v bm4=%v", remote.applied["bm1"], remote.applied["bm4"])
	}
	gs := res.Status.Grants["0050"]
	if gs.Status != "active" || gs.Servers["bm4"] != "ok" || !strings.Contains(gs.Servers["bm1"], "2 admin approvals") {
		t.Fatalf("grant status after growth: %+v", gs)
	}

	// bm3 leaves the group: the account goes, the certificate stays for the rest.
	in.Policy, in.State, in.Certs = groupPolicy(t, "  puku-bot: [bm1, bm4]\n"), res.State, res.Certs
	res, _ = Apply(context.Background(), in)
	if users(remote.applied["bm3"]) != "" || users(remote.applied["bm4"]) != "rakib" || string(res.KRL) != "[]" {
		t.Fatalf("after shrink: bm3=%v bm4=%v krl=%s", remote.applied["bm3"], remote.applied["bm4"], res.KRL)
	}

	// Only production left, approved by one admin: nothing applied, nothing revoked yet... the grant waits.
	in.Policy, in.State, in.Certs = groupPolicy(t, "  puku-bot: [bm1]\n"), res.State, res.Certs
	res, _ = Apply(context.Background(), in)
	if res.Status.Grants["0050"].Status != "awaiting_approval" || users(remote.applied["bm1"]) != "" || users(remote.applied["bm4"]) != "" {
		t.Fatalf("production-only group: %+v bm1=%v", res.Status.Grants["0050"], remote.applied["bm1"])
	}

	// With two approvals, a production group member is covered.
	in.State, in.Verifier = nil, fakeVerifier{"0050": 2}
	res, _ = Apply(context.Background(), in)
	if users(remote.applied["bm1"]) != "rakib" || res.Status.Grants["0050"].Servers["bm1"] != "ok" {
		t.Fatalf("two approvals: bm1=%v %+v", remote.applied["bm1"], res.Status.Grants["0050"])
	}
}
