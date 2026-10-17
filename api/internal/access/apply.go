package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// This file is the apply engine run by the bm2 runner after a merge (and daily).
// It never trusts the grants file alone: every grant must be backed by a merged
// pull request with enough admin approvals on its final commit (the Verifier),
// because a free GitHub plan cannot enforce branch protection on private repos.

// State is persisted between runs on the access-state branch.
type State struct {
	Approved map[string]string     `json:"approved"` // grant id → hash of the approved grant
	Issued   map[string]IssuedCert `json:"issued"`   // grant id → certificate issued for it
}

type IssuedCert struct {
	Serial  uint64    `json:"serial"`
	Expires time.Time `json:"expires"`
}

// Status is published on the access-state branch for the dashboard to read.
type Status struct {
	GeneratedAt time.Time               `json:"generated_at"`
	Commit      string                  `json:"commit"`
	Grants      map[string]GrantStatus  `json:"grants"`
	Servers     map[string]ServerStatus `json:"servers"`
}

type GrantStatus struct {
	Status      string            `json:"status"` // active | awaiting_approval | expired | not_started
	Reason      string            `json:"reason,omitempty"`
	CertExpires *time.Time        `json:"cert_expires,omitempty"`
	Servers     map[string]string `json:"servers,omitempty"` // server → "ok" | error message
}

type ServerStatus struct {
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Accounts  int       `json:"accounts"`
	AppliedAt time.Time `json:"applied_at"`
}

// Verifier confirms that a grant was approved through a merged pull request.
type Verifier interface {
	Verify(ctx context.Context, g Grant) (ok bool, reason string, err error)
}

// Remote applies the desired accounts and revocation list to one server.
type Remote interface {
	Apply(ctx context.Context, name string, srv Server, accounts []Account, krl []byte) error
}

// KRLMaker builds an OpenSSH key revocation list revoking the given serials.
type KRLMaker func(caPub ssh.PublicKey, serials []uint64) ([]byte, error)

// ApplyInput is everything one run needs.
type ApplyInput struct {
	File     *File
	Policy   *Policy
	State    *State
	Certs    map[string][]byte // grant id → existing certificate line
	CA       ssh.Signer
	Verifier Verifier
	Remote   Remote
	KRL      KRLMaker
	Now      time.Time
	Commit   string
	DryRun   bool // compute and sign, but do not touch servers
}

// ApplyResult is what the caller persists.
type ApplyResult struct {
	State  *State
	Status Status
	Certs  map[string][]byte // certificates that must exist after the run
	KRL    []byte
}

// GrantHash identifies the exact approved content of a grant.
func GrantHash(g Grant) string {
	g.RequestedAt, g.Expires = g.RequestedAt.UTC(), g.Expires.UTC()
	sort.Strings(g.Servers)
	b, _ := json.Marshal(g)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Apply runs one reconciliation. Errors on individual servers are recorded in
// the status and do not stop the others.
func Apply(ctx context.Context, in ApplyInput) (*ApplyResult, error) {
	st := &State{Approved: map[string]string{}, Issued: map[string]IssuedCert{}}
	if in.State != nil {
		for k, v := range in.State.Approved {
			st.Approved[k] = v
		}
		for k, v := range in.State.Issued {
			st.Issued[k] = v
		}
	}
	res := &ApplyResult{State: st, Certs: map[string][]byte{},
		Status: Status{GeneratedAt: in.Now.UTC(), Commit: in.Commit, Grants: map[string]GrantStatus{}, Servers: map[string]ServerStatus{}}}

	// 1. Only grants backed by an approved, merged pull request count.
	inFile := map[string]bool{}
	var effective []Grant
	for _, g := range in.File.Grants {
		inFile[g.ID] = true
		h := GrantHash(g)
		if st.Approved[g.ID] != h {
			ok, reason, err := in.Verifier.Verify(ctx, g)
			if err != nil {
				return nil, fmt.Errorf("verify grant %s: %w", g.ID, err)
			}
			if !ok {
				res.Status.Grants[g.ID] = GrantStatus{Status: "awaiting_approval", Reason: reason}
				continue
			}
			st.Approved[g.ID] = h
		}
		effective = append(effective, g)
	}
	for id := range st.Approved {
		if !inFile[id] {
			delete(st.Approved, id)
		}
	}

	// 2. Certificates for active grants.
	active := map[string]bool{}
	for _, g := range effective {
		switch {
		case !in.Now.Before(g.Expires):
			res.Status.Grants[g.ID] = GrantStatus{Status: "expired"}
			continue
		case in.Now.Before(g.RequestedAt):
			res.Status.Grants[g.ID] = GrantStatus{Status: "not_started"}
			continue
		}
		cert := in.Certs[g.ID]
		if cert == nil || !CertMatches(cert, in.CA.PublicKey(), g) {
			c, err := SignUserCert(in.CA, g)
			if err != nil {
				return nil, err
			}
			cert = c
		}
		serial, _ := strconv.ParseUint(g.ID, 10, 64)
		st.Issued[g.ID] = IssuedCert{Serial: serial, Expires: g.Expires.UTC()}
		res.Certs[g.ID] = cert
		active[g.ID] = true
		exp := g.Expires.UTC()
		res.Status.Grants[g.ID] = GrantStatus{Status: "active", CertExpires: &exp, Servers: map[string]string{}}
	}

	// 3. Revoke still-valid certificates whose grant is no longer active
	//    (removed by a revoke pull request). Expired ones need nothing.
	var revoked []uint64
	for id, ic := range st.Issued {
		switch {
		case !in.Now.Before(ic.Expires):
			delete(st.Issued, id) // expired on its own; forget it
		case !active[id]:
			revoked = append(revoked, ic.Serial)
		}
	}
	sort.Slice(revoked, func(i, j int) bool { return revoked[i] < revoked[j] })
	krl, err := in.KRL(in.CA.PublicKey(), revoked)
	if err != nil {
		return nil, fmt.Errorf("revocation list: %w", err)
	}
	res.KRL = krl

	// 4. Accounts on every server.
	desired := Desired(&File{Grants: effective}, in.Policy, in.Now)
	names := make([]string, 0, len(in.Policy.Servers))
	for n := range in.Policy.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ss := ServerStatus{Accounts: len(desired[name]), AppliedAt: in.Now.UTC()}
		if in.DryRun {
			ss.Error = "dry run: not applied"
		} else if err := in.Remote.Apply(ctx, name, in.Policy.Servers[name], desired[name], krl); err != nil {
			ss.Error = err.Error()
		} else {
			ss.OK = true
		}
		res.Status.Servers[name] = ss
	}
	for _, g := range effective {
		gs, ok := res.Status.Grants[g.ID]
		if !ok || gs.Status != "active" {
			continue
		}
		for _, s := range g.Servers {
			if ss := res.Status.Servers[s]; ss.OK {
				gs.Servers[s] = "ok"
			} else {
				gs.Servers[s] = ss.Error
			}
		}
		res.Status.Grants[g.ID] = gs
	}
	return res, nil
}
