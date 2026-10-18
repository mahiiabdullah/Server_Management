package access

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// SignUserCert issues an OpenSSH user certificate for g's public key: principal
// = the Unix account, key id = "<user>-<grant id>" (appears in sshd logs),
// serial = the grant id (used for revocation), valid from requested_at - 5 min
// (clock skew) to expires.
func SignUserCert(ca ssh.Signer, g Grant) ([]byte, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(g.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("grant %s: %w", g.ID, err)
	}
	serial, err := strconv.ParseUint(g.ID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("grant %s: id is not numeric", g.ID)
	}
	cert := &ssh.Certificate{
		Key:             pub,
		Serial:          serial,
		CertType:        ssh.UserCert,
		KeyId:           g.User + "-" + g.ID,
		ValidPrincipals: []string{g.User},
		ValidAfter:      uint64(g.RequestedAt.Add(-5 * time.Minute).Unix()),
		ValidBefore:     uint64(g.Expires.Unix()),
		Permissions: ssh.Permissions{Extensions: map[string]string{
			"permit-pty":              "",
			"permit-user-rc":          "",
			"permit-agent-forwarding": "",
			"permit-port-forwarding":  "",
		}},
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		return nil, err
	}
	return ssh.MarshalAuthorizedKey(cert), nil
}

// CertMatches reports whether an existing certificate still matches g (same
// key, principal, serial and validity), so apply can skip re-signing.
func CertMatches(certLine []byte, caPub ssh.PublicKey, g Grant) bool {
	pub, _, _, _, err := ssh.ParseAuthorizedKey(certLine)
	if err != nil {
		return false
	}
	c, ok := pub.(*ssh.Certificate)
	if !ok {
		return false
	}
	want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(g.PublicKey))
	if err != nil {
		return false
	}
	serial, _ := strconv.ParseUint(g.ID, 10, 64)
	return string(c.SignatureKey.Marshal()) == string(caPub.Marshal()) &&
		string(c.Key.Marshal()) == string(want.Marshal()) &&
		c.Serial == serial && len(c.ValidPrincipals) == 1 && c.ValidPrincipals[0] == g.User &&
		c.ValidBefore == uint64(g.Expires.Unix())
}

// Account is the desired state of one managed Unix account on one server.
type Account struct {
	User string
	Sudo bool
}

// Desired computes, for every server in the policy, the managed accounts that
// should exist at time now. A user with several active grants on a server gets
// sudo if any of them grants it. Groups expand to their current servers.
func Desired(f *File, p *Policy, now time.Time) map[string][]Account {
	return DesiredOn(f.Grants, p.ServersOf, p, now)
}

// DesiredOn is Desired with the servers of each grant chosen by serversOf.
func DesiredOn(grants []Grant, serversOf func(Grant) []string, p *Policy, now time.Time) map[string][]Account {
	type key struct{ server, user string }
	sudo := map[key]bool{}
	for _, g := range grants {
		if !g.Active(now) {
			continue
		}
		for _, s := range serversOf(g) {
			k := key{s, g.User}
			sudo[k] = sudo[k] || g.Sudo
		}
	}
	out := map[string][]Account{}
	for name := range p.Servers {
		out[name] = []Account{}
	}
	for k, s := range sudo {
		if _, ok := out[k.server]; ok {
			out[k.server] = append(out[k.server], Account{User: k.user, Sudo: s})
		}
	}
	for name := range out {
		sort.Slice(out[name], func(i, j int) bool { return out[name][i].User < out[name][j].User })
	}
	return out
}
