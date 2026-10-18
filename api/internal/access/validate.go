package access

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	idRe   = regexp.MustCompile(`^[0-9]{4,9}$`)
	userRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,30}$`)
)

// Always reserved, whatever the policy says: never hand these out.
var builtinReserved = []string{"root", "admin", "ubuntu", "daemon", "bin", "sys", "sync", "nobody", "sshd",
	"node_exporter", "postgres", "www-data", "systemd-network", "messagebus", "syslog"}

// KeyInfo is a checked public key.
type KeyInfo struct {
	Normalized  string // "type base64 comment"
	Fingerprint string // SHA256:…
	Type        string
}

// ParsePublicKey accepts one OpenSSH public key line. It refuses private keys,
// certificates and weak key types.
func ParsePublicKey(line string) (*KeyInfo, error) {
	line = strings.TrimSpace(line)
	if strings.Contains(line, "PRIVATE KEY") {
		return nil, errors.New("this is a PRIVATE key: never share it. Paste the contents of your .pub file instead")
	}
	if strings.Count(line, "\n") > 0 {
		return nil, errors.New("paste exactly one public key line")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, errors.New("not a valid OpenSSH public key (expected e.g. \"ssh-ed25519 AAAA… you@laptop\")")
	}
	if len(options) > 0 {
		return nil, errors.New("remove the options before the key type")
	}
	if _, isCert := key.(*ssh.Certificate); isCert {
		return nil, errors.New("this is a certificate; paste your plain public key")
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		ck, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return nil, errors.New("unreadable RSA key")
		}
		pk, ok := ck.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || pk.N.BitLen() < 3072 {
			return nil, errors.New("RSA keys must be at least 3072 bits; ed25519 is recommended (ssh-keygen -t ed25519)")
		}
	default:
		return nil, fmt.Errorf("key type %s is not allowed; use ed25519 (ssh-keygen -t ed25519)", key.Type())
	}
	comment = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(comment))
	if len(comment) > 64 {
		comment = comment[:64]
	}
	norm := strings.TrimSpace(key.Type() + " " + strings.Fields(string(ssh.MarshalAuthorizedKey(key)))[1] + " " + comment)
	return &KeyInfo{Normalized: norm, Fingerprint: ssh.FingerprintSHA256(key), Type: key.Type()}, nil
}

// UsernameFromEmail derives the Unix account name from the email's local part
// (decision D4): lowercase, letters and digits only, starting with a letter.
func UsernameFromEmail(email string) (string, error) {
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return "", errors.New("invalid email")
	}
	local := strings.ToLower(addr.Address[:strings.LastIndex(addr.Address, "@")])
	var b strings.Builder
	for _, r := range local {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	u := strings.TrimLeft(b.String(), "0123456789")
	if len(u) > 31 {
		u = u[:31]
	}
	if !userRe.MatchString(u) {
		return "", fmt.Errorf("cannot derive a Unix username from %q", email)
	}
	return u, nil
}

// Problem is one validation failure, tied to a grant when there is one.
type Problem struct {
	GrantID string `json:"grant_id,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	switch {
	case p.GrantID != "" && p.Field != "":
		return fmt.Sprintf("grant %s, %s: %s", p.GrantID, p.Field, p.Message)
	case p.GrantID != "":
		return fmt.Sprintf("grant %s: %s", p.GrantID, p.Message)
	}
	return p.Message
}

// Problems collects validation failures.
type Problems []Problem

func (ps Problems) Error() string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, "\n")
}

func (p *Policy) reserved(user string) bool {
	for _, r := range append(builtinReserved, p.ReservedUsers...) {
		if r == user {
			return true
		}
	}
	return false
}

// ValidateGrant checks one grant against the policy. isNew applies the rules
// that only make sense when a grant is created or changed (it must not already
// be expired, and its duration is checked from now).
func (p *Policy) ValidateGrant(g Grant, now time.Time, isNew bool) Problems {
	var ps Problems
	bad := func(field, msg string) { ps = append(ps, Problem{GrantID: g.ID, Field: field, Message: msg}) }

	if !idRe.MatchString(g.ID) {
		bad("id", "must be 4-9 digits")
	}
	if !userRe.MatchString(g.User) {
		bad("user", "must be a lowercase Unix name (a-z, 0-9, _ or -), 2-31 characters, starting with a letter")
	} else if p.reserved(g.User) {
		bad("user", fmt.Sprintf("%q is reserved", g.User))
	}
	if want, err := UsernameFromEmail(g.Email); err != nil {
		bad("email", err.Error())
	} else if want != g.User {
		bad("user", fmt.Sprintf("must be %q (derived from the email)", want))
	}
	if len(g.Servers) == 0 && len(g.Groups) == 0 {
		bad("servers", "choose at least one server or group")
	}
	seen := map[string]bool{}
	for _, s := range g.Servers {
		if _, ok := p.Servers[s]; !ok {
			bad("servers", fmt.Sprintf("unknown server %q", s))
		} else if isNew && !p.Servers[s].IsReady() {
			bad("servers", fmt.Sprintf("%q is not ready for SSH access yet (server setup not done)", s))
		}
		if seen[s] {
			bad("servers", fmt.Sprintf("%q listed twice", s))
		}
		seen[s] = true
	}
	seenGroup := map[string]bool{}
	for _, grp := range g.Groups {
		if _, ok := p.Groups[grp]; !ok {
			bad("groups", fmt.Sprintf("unknown access group %q", grp))
		}
		if seenGroup[grp] {
			bad("groups", fmt.Sprintf("%q listed twice", grp))
		}
		seenGroup[grp] = true
	}
	if g.Sudo && len(g.Groups) > 0 {
		bad("sudo", "sudo cannot be requested for a group; request it for specific servers")
	}
	if isNew && len(g.Groups) > 0 && len(p.ServersOf(g)) == 0 {
		bad("groups", "these groups have no servers yet")
	}
	if r := strings.TrimSpace(g.Reason); len(r) < 10 || len(r) > 300 {
		bad("reason", "must be 10-300 characters")
	}
	if _, err := ParsePublicKey(g.PublicKey); err != nil {
		bad("public_key", err.Error())
	}
	if g.RequestedAt.IsZero() || g.Expires.IsZero() {
		bad("expires", "requested_at and expires are required")
	} else {
		if !g.Expires.After(g.RequestedAt) {
			bad("expires", "must be after requested_at")
		}
		limit := p.MaxDays
		if g.Sudo {
			limit = p.MaxSudoDays
		}
		if g.Expires.Sub(g.RequestedAt) > time.Duration(limit)*24*time.Hour+time.Minute {
			bad("expires", fmt.Sprintf("access can last at most %d days%s", limit, map[bool]string{true: " with sudo", false: ""}[g.Sudo]))
		}
		if isNew {
			if !g.Expires.After(now) {
				bad("expires", "is already in the past")
			}
			if g.RequestedAt.After(now.Add(10 * time.Minute)) {
				bad("requested_at", "is in the future")
			}
			if now.Sub(g.RequestedAt) > 7*24*time.Hour {
				bad("requested_at", "is more than 7 days old; open a new request")
			}
		}
	}
	return ps
}

// ValidateFile checks file-wide invariants: unique ids, one email per user,
// and no public key shared between different people.
func (p *Policy) ValidateFile(f *File) Problems {
	var ps Problems
	ids := map[string]bool{}
	userEmail := map[string]string{}
	keyUser := map[string]string{}
	for _, g := range f.Grants {
		if ids[g.ID] {
			ps = append(ps, Problem{GrantID: g.ID, Field: "id", Message: "duplicate id"})
		}
		ids[g.ID] = true
		if e, ok := userEmail[g.User]; ok && !strings.EqualFold(e, g.Email) {
			ps = append(ps, Problem{GrantID: g.ID, Field: "user", Message: fmt.Sprintf("user %q already belongs to %s", g.User, e)})
		}
		userEmail[g.User] = g.Email
		if k, err := ParsePublicKey(g.PublicKey); err == nil {
			if u, ok := keyUser[k.Fingerprint]; ok && u != g.User {
				ps = append(ps, Problem{GrantID: g.ID, Field: "public_key", Message: "this key is already used by another person"})
			}
			keyUser[k.Fingerprint] = g.User
		}
	}
	return ps
}

// Change is the difference between two versions of the grants file.
type Change struct {
	Added   []Grant
	Removed []Grant
	Changed []Grant // new versions
	Old     map[string]Grant
}

// Diff compares base and head by grant id.
func Diff(base, head *File) Change {
	c := Change{Old: map[string]Grant{}}
	b := map[string]Grant{}
	for _, g := range base.Grants {
		b[g.ID] = g
	}
	h := map[string]bool{}
	for _, g := range head.Grants {
		h[g.ID] = true
		old, ok := b[g.ID]
		switch {
		case !ok:
			c.Added = append(c.Added, g)
		case !sameGrant(old, g):
			c.Changed = append(c.Changed, g)
			c.Old[g.ID] = old
		}
	}
	for _, g := range base.Grants {
		if !h[g.ID] {
			c.Removed = append(c.Removed, g)
		}
	}
	return c
}

func sameGrant(a, b Grant) bool {
	if a.ID != b.ID || a.User != b.User || a.Email != b.Email || a.Sudo != b.Sudo || a.Reason != b.Reason ||
		a.PublicKey != b.PublicKey || !a.RequestedAt.Equal(b.RequestedAt) || !a.Expires.Equal(b.Expires) {
		return false
	}
	return sameSet(a.Servers, b.Servers) && sameSet(a.Groups, b.Groups)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// Kind of pull request, from the label the dashboard sets.
const (
	KindRequest = "request" // adds exactly one new grant
	KindRevoke  = "revoke"  // removes exactly one grant
)

// PRCheck is the verdict on a pull request that edits grants.yaml.
type PRCheck struct {
	Kind              string   `json:"kind"`
	GrantID           string   `json:"grant_id"`
	RequesterEmail    string   `json:"requester_email"`
	RequiredApprovals int      `json:"required_approvals"`
	Problems          Problems `json:"problems"`
}

// CheckPR validates the change from base to head for a pull request of the given kind.
func (p *Policy) CheckPR(base, head *File, kind string, now time.Time) PRCheck {
	res := PRCheck{Kind: kind, RequiredApprovals: 1}
	fail := func(msg string) { res.Problems = append(res.Problems, Problem{Message: msg}) }
	res.Problems = append(res.Problems, p.ValidateFile(head)...)
	c := Diff(base, head)

	switch kind {
	case KindRequest:
		if len(c.Removed) > 0 {
			fail("a request must not remove grants (use a revoke pull request)")
		}
		if len(c.Changed) > 0 {
			fail("a request must not change existing grants; an extension is a new request")
		}
		if len(c.Added) != 1 {
			fail(fmt.Sprintf("a request must add exactly one grant; this one adds %d", len(c.Added)))
			break
		}
		g := c.Added[0]
		res.GrantID, res.RequesterEmail = g.ID, g.Email
		res.Problems = append(res.Problems, p.ValidateGrant(g, now, true)...)
		res.RequiredApprovals = p.RequiredApprovals(g)
	case KindRevoke:
		if len(c.Added) > 0 || len(c.Changed) > 0 {
			fail("a revoke must only remove a grant")
		}
		if len(c.Removed) != 1 {
			fail(fmt.Sprintf("a revoke must remove exactly one grant; this one removes %d", len(c.Removed)))
		} else {
			// No requester: removing access is never self-serving, so the grant
			// owner's own approval counts too (e.g. a sole admin revoking their grant).
			res.GrantID = c.Removed[0].ID
		}
	default:
		fail(fmt.Sprintf("unknown pull request kind %q (label it access-request or access-revoke)", kind))
	}
	return res
}

// CountApprovals counts approvals that satisfy the policy: from admins only,
// on the final commit, and never from the requester themselves.
func (p *Policy) CountApprovals(reviews []Review, headSHA, requesterEmail string) (n int, ignored []string) {
	latest := map[string]Review{}
	for _, r := range reviews { // reviews arrive in chronological order; keep each reviewer's last word
		if r.State == "COMMENTED" {
			continue
		}
		latest[strings.ToLower(r.User)] = r
	}
	for login, r := range latest {
		a := p.AdminByGitHub(login)
		switch {
		case r.State != "APPROVED":
		case a == nil:
			ignored = append(ignored, login+" (not an admin)")
		case r.CommitID != headSHA:
			ignored = append(ignored, login+" (approved an earlier version)")
		case strings.EqualFold(a.Email, requesterEmail):
			ignored = append(ignored, login+" (cannot approve their own request)")
		default:
			n++
		}
	}
	sort.Strings(ignored)
	return n, ignored
}

// PolicyApprovals counts approvals of a pull request that changes the policy:
// from admins of the policy before the change, on the final commit, never from
// its author. For a pull request the dashboard bot opened, the admin named on
// its "Requested by:" line counts as the author.
func PolicyApprovals(base *Policy, reviews []Review, headSHA, author, body string) (int, []string) {
	exclude := ""
	if a := base.AdminByGitHub(author); a != nil {
		exclude = a.Email
	} else if strings.HasSuffix(author, "[bot]") {
		for _, line := range strings.Split(body, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Requested by:"); ok {
				exclude = strings.TrimSpace(v)
				break
			}
		}
	}
	return base.CountApprovals(reviews, headSHA, exclude)
}

// Review is one pull-request review, as GitHub reports it.
type Review struct {
	User     string `json:"user"`
	State    string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	CommitID string `json:"commit_id"`
}
