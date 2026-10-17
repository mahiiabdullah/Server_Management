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

type fakeVerifier map[string]bool // grant id → approved

func (f fakeVerifier) Verify(_ context.Context, g Grant) (bool, string, error) {
	if f[g.ID] {
		return true, "", nil
	}
	return false, "no approved pull request", nil
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
		Verifier: fakeVerifier{"0041": true, "0042": true}, Remote: remote, KRL: fakeKRL, Now: now, Commit: "abc"}

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
		Verifier: fakeVerifier{"0041": true}, Remote: remote, KRL: fakeKRL, Now: now})
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
	in := ApplyInput{File: &File{Grants: []Grant{a}}, Policy: p, CA: newCA(t), Verifier: fakeVerifier{"0041": true},
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
