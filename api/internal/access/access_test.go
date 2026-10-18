package access

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func edKey(t *testing.T, comment string) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sp, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment, priv
}

func policy() *Policy {
	p, err := ParsePolicy([]byte(`
servers:
  bm1: {host: 10.0.0.11, ssh_user: root, production: true}
  bm3: {host: 10.0.0.13, ssh_user: root, production: false}
max_days: 30
max_sudo_days: 7
admins:
  - {github: alice-gh, email: alice@example.com}
  - {github: bob-gh, email: bob@example.com}
reserved_users: [mahi]
`))
	if err != nil {
		panic(err)
	}
	return p
}

func grant(t *testing.T, id, email string, servers ...string) Grant {
	k, _ := edKey(t, "laptop")
	u, _ := UsernameFromEmail(email)
	return Grant{ID: id, User: u, Email: email, Servers: servers, Reason: "Debug the sandbox service",
		PublicKey: k, RequestedAt: now, Expires: now.Add(30 * 24 * time.Hour)}
}

func TestParsePublicKey(t *testing.T) {
	ed, _ := edKey(t, "me@laptop")
	if k, err := ParsePublicKey(ed); err != nil || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("ed25519 rejected: %v", err)
	}
	small, _ := rsa.GenerateKey(rand.Reader, 2048)
	sp, _ := ssh.NewPublicKey(&small.PublicKey)
	if _, err := ParsePublicKey(string(ssh.MarshalAuthorizedKey(sp))); err == nil || !strings.Contains(err.Error(), "3072") {
		t.Errorf("2048-bit RSA accepted: %v", err)
	}
	for in, want := range map[string]string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabc": "PRIVATE",
		"ssh-ed25519 notbase64":                    "not a valid",
		`no-pty ` + ed:                             "options",
		ed + "\n" + ed:                             "exactly one",
	} {
		if _, err := ParsePublicKey(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.30q: got %v, want error containing %q", in, err, want)
		}
	}
}

func TestUsernameFromEmail(t *testing.T) {
	for in, want := range map[string]string{
		"rakib@example.com":            "rakib",
		"Md.Rakib_Hasan@example.com":   "mdrakibhasan",
		"2003mahi@gmail.com":           "mahi",
		"mdmahiabdullah2003@gmail.com": "mdmahiabdullah2003",
	} {
		if got, err := UsernameFromEmail(in); err != nil || got != want {
			t.Errorf("%s → %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := UsernameFromEmail("123@x.com"); err == nil {
		t.Error("digits-only local part accepted")
	}
}

func TestValidateGrant(t *testing.T) {
	p := policy()
	g := grant(t, "0042", "rakib@example.com", "bm3")
	if ps := p.ValidateGrant(g, now, true); len(ps) > 0 {
		t.Fatalf("valid grant rejected: %v", ps)
	}
	cases := map[string]func(*Grant){
		"must be \"rakib\"":        func(g *Grant) { g.User = "root2" },
		"reserved":                 func(g *Grant) { g.User, g.Email = "mahi", "mahi@x.com" },
		"unknown server":           func(g *Grant) { g.Servers = []string{"bm9"} },
		"at most 30 days":          func(g *Grant) { g.Expires = now.Add(31 * 24 * time.Hour) },
		"at most 7 days with sudo": func(g *Grant) { g.Sudo = true },
		"already in the past":      func(g *Grant) { g.RequestedAt, g.Expires = now.Add(-48*time.Hour), now.Add(-time.Hour) },
		"10-300 characters":        func(g *Grant) { g.Reason = "fix" },
		"4-9 digits":               func(g *Grant) { g.ID = "42" },
	}
	for want, mut := range cases {
		x := g
		mut(&x)
		if ps := p.ValidateGrant(x, now, true); !strings.Contains(ps.Error(), want) {
			t.Errorf("want %q, got %q", want, ps.Error())
		}
	}
}

func TestCheckPRRequest(t *testing.T) {
	p := policy()
	a := grant(t, "0041", "alice@example.com", "bm3")
	base := &File{Grants: []Grant{a}}

	r := grant(t, "0042", "rakib@example.com", "bm3")
	res := p.CheckPR(base, &File{Grants: []Grant{a, r}}, KindRequest, now)
	if len(res.Problems) > 0 || res.GrantID != "0042" || res.RequiredApprovals != 1 || res.RequesterEmail != "rakib@example.com" {
		t.Fatalf("simple request: %+v", res)
	}

	prod := grant(t, "0042", "rakib@example.com", "bm1")
	if res := p.CheckPR(base, &File{Grants: []Grant{a, prod}}, KindRequest, now); res.RequiredApprovals != 2 {
		t.Errorf("production server needs 2 approvals, got %d", res.RequiredApprovals)
	}

	// Touching someone else's entry in the shared file is refused.
	a2 := a
	a2.Servers = []string{"bm1", "bm3"}
	if res := p.CheckPR(base, &File{Grants: []Grant{a2, r}}, KindRequest, now); len(res.Problems) == 0 {
		t.Error("request that also changes another grant was accepted")
	}
	if res := p.CheckPR(base, &File{Grants: []Grant{r}}, KindRequest, now); len(res.Problems) == 0 {
		t.Error("request that removes another grant was accepted")
	}
	// A request may not take over or extend an existing grant.
	stolen := a
	stolen.User, stolen.Email = "rakib", "rakib@example.com"
	if res := p.CheckPR(base, &File{Grants: []Grant{stolen}}, KindRequest, now); len(res.Problems) == 0 {
		t.Error("changing the user of an existing grant was accepted")
	}
	longer := a
	longer.Expires = longer.Expires.Add(time.Hour)
	if res := p.CheckPR(base, &File{Grants: []Grant{longer}}, KindRequest, now); len(res.Problems) == 0 {
		t.Error("extending an existing grant in place was accepted")
	}
	// The same key cannot belong to two people.
	dup := r
	dup.PublicKey = a.PublicKey
	if res := p.CheckPR(base, &File{Grants: []Grant{a, dup}}, KindRequest, now); !strings.Contains(res.Problems.Error(), "another person") {
		t.Errorf("shared key accepted: %v", res.Problems)
	}
}

func TestCheckPRRevoke(t *testing.T) {
	p := policy()
	a, r := grant(t, "0041", "alice@example.com", "bm3"), grant(t, "0042", "rakib@example.com", "bm3")
	base := &File{Grants: []Grant{a, r}}
	if res := p.CheckPR(base, &File{Grants: []Grant{a}}, KindRevoke, now); len(res.Problems) > 0 || res.GrantID != "0042" {
		t.Fatalf("revoke: %+v", res)
	}
	if res := p.CheckPR(base, &File{}, KindRevoke, now); len(res.Problems) == 0 {
		t.Error("revoke removing two grants accepted")
	}
}

func TestCountApprovals(t *testing.T) {
	p := policy()
	reviews := []Review{
		{User: "alice-gh", State: "APPROVED", CommitID: "old"},
		{User: "alice-gh", State: "APPROVED", CommitID: "head"},
		{User: "bob-gh", State: "APPROVED", CommitID: "old"},   // stale
		{User: "mallory", State: "APPROVED", CommitID: "head"}, // not an admin
		{User: "bob-gh", State: "COMMENTED", CommitID: "head"}, // comment does not reset
	}
	n, ignored := p.CountApprovals(reviews, "head", "rakib@example.com")
	if n != 1 || len(ignored) != 2 {
		t.Fatalf("n=%d ignored=%v", n, ignored)
	}
	// An admin cannot approve their own request.
	if n, _ := p.CountApprovals(reviews, "head", "alice@example.com"); n != 0 {
		t.Errorf("self-approval counted")
	}
	// Changes requested after an approval cancels it.
	reviews = append(reviews, Review{User: "alice-gh", State: "CHANGES_REQUESTED", CommitID: "head"})
	if n, _ := p.CountApprovals(reviews, "head", "rakib@example.com"); n != 0 {
		t.Errorf("approval counted after changes were requested")
	}
}

func TestMarshalRoundTripAndCanonicalOrder(t *testing.T) {
	f := &File{Grants: []Grant{grant(t, "0043", "b@example.com", "bm3"), grant(t, "0041", "a@example.com", "bm1", "bm3")}}
	data, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# SSH access grants") || strings.Index(string(data), "0041") > strings.Index(string(data), "0043") {
		t.Fatalf("not canonical:\n%s", data)
	}
	back, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if c := Diff(&File{Grants: []Grant{f.Grants[1], f.Grants[0]}}, back); len(c.Added)+len(c.Removed)+len(c.Changed) != 0 {
		t.Fatalf("round trip changed grants: %+v", c)
	}
	if _, err := ParseFile([]byte("grants:\n  - id: \"1\"\n    surprise: true\n")); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestSignUserCert(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca, _ := ssh.NewSignerFromKey(caPriv)
	g := grant(t, "0042", "rakib@example.com", "bm3")
	line, err := SignUserCert(ca, g)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(line)
	if err != nil {
		t.Fatal(err)
	}
	cert := pub.(*ssh.Certificate)
	checker := ssh.CertChecker{
		IsUserAuthority: func(k ssh.PublicKey) bool { return string(k.Marshal()) == string(ca.PublicKey().Marshal()) },
		Clock:           func() time.Time { return now.Add(time.Hour) },
	}
	if err := checker.CheckCert("rakib", cert); err != nil {
		t.Fatalf("certificate rejected for its own user: %v", err)
	}
	if err := checker.CheckCert("root", cert); err == nil {
		t.Error("certificate accepted for root")
	}
	checker.Clock = func() time.Time { return g.Expires.Add(time.Minute) }
	if err := checker.CheckCert("rakib", cert); err == nil {
		t.Error("certificate accepted after expiry")
	}
	if cert.KeyId != "rakib-0042" || cert.Serial != 42 {
		t.Errorf("key id %q serial %d", cert.KeyId, cert.Serial)
	}
	if !CertMatches(line, ca.PublicKey(), g) {
		t.Error("CertMatches false for its own grant")
	}
	g.Expires = g.Expires.Add(time.Hour)
	if CertMatches(line, ca.PublicKey(), g) {
		t.Error("CertMatches true after the grant changed")
	}
}

func TestDesired(t *testing.T) {
	p := policy()
	a := grant(t, "0041", "alice@example.com", "bm1", "bm3")
	b := grant(t, "0042", "alice@example.com", "bm3")
	b.Sudo, b.Expires = true, now.Add(24*time.Hour)
	old := grant(t, "0040", "carol@example.com", "bm3")
	old.RequestedAt, old.Expires = now.Add(-48*time.Hour), now.Add(-time.Hour)
	d := Desired(&File{Grants: []Grant{a, b, old}}, p, now)
	if len(d["bm1"]) != 1 || d["bm1"][0].Sudo {
		t.Errorf("bm1: %+v", d["bm1"])
	}
	if len(d["bm3"]) != 1 || !d["bm3"][0].Sudo {
		t.Errorf("bm3 (sudo from the second grant, expired carol absent): %+v", d["bm3"])
	}
}

func TestGroupRules(t *testing.T) {
	p := groupPolicy(t, "  puku-bot: [bm3, bm4]\n  ops: [bm1, bm3]\n  empty: []\n")
	g := grant(t, "0060", "rakib@example.com")
	g.Groups = []string{"puku-bot", "ops"}
	if got := strings.Join(p.ServersOf(g), ","); got != "bm1,bm3,bm4" {
		t.Errorf("servers of = %s", got)
	}
	if !p.NeedsTwoApprovals(g) {
		t.Error("a group with a production server must need two approvals")
	}
	g.Groups = []string{"puku-bot"}
	if p.NeedsTwoApprovals(g) || len(p.ValidateGrant(g, now, true)) != 0 {
		t.Errorf("valid group grant: two=%v problems=%v", p.NeedsTwoApprovals(g), p.ValidateGrant(g, now, true))
	}
	for name, mutate := range map[string]func(*Grant){
		"sudo on a group": func(x *Grant) { x.Sudo = true; x.Expires = now.Add(24 * time.Hour) },
		"unknown group":   func(x *Grant) { x.Groups = []string{"nope"} },
		"empty group":     func(x *Grant) { x.Groups = []string{"empty"} },
		"twice":           func(x *Grant) { x.Groups = []string{"puku-bot", "puku-bot"} },
		"nothing":         func(x *Grant) { x.Groups = nil },
	} {
		x := g
		mutate(&x)
		if len(p.ValidateGrant(x, now, true)) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParsePolicy([]byte("servers: {bm3: {host: h}}\nmax_days: 3\nmax_sudo_days: 1\nadmins: [{github: a, email: a@x.io}]\ngroups: {g: [bm9]}\n")); err == nil {
		t.Error("group with an unknown server accepted")
	}

	// A request PR that adds a group grant passes the same CI rules.
	base := &File{}
	if res := p.CheckPR(base, &File{Grants: []Grant{g}}, KindRequest, now); len(res.Problems) > 0 || res.RequiredApprovals != 1 {
		t.Errorf("check = %+v", res)
	}
	// The grant file round-trips groups, and they are part of the approved content.
	b, _ := (&File{Grants: []Grant{g}}).Marshal()
	f, err := ParseFile(b)
	if err != nil || len(f.Grants[0].Groups) != 1 || !strings.Contains(string(b), "groups:") || strings.Contains(string(b), "servers:") {
		t.Fatalf("round trip: %v\n%s", err, b)
	}
	other := g
	other.Groups = []string{"ops"}
	if GrantHash(g) == GrantHash(other) {
		t.Error("changing a grant's group does not change its hash")
	}
}
