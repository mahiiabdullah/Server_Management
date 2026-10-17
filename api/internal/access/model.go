// Package access implements the SSH access workflow: the shared grants file,
// the access policy, request validation, pull-request checks and SSH
// certificate signing. The same code runs in the dashboard (to build requests),
// in CI (to validate pull requests) and on bm2 (to apply grants), so all three
// enforce identical rules.
package access

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Paths inside the repository.
const (
	GrantsPath = "access/grants.yaml"
	PolicyPath = "access/policy.yaml"
)

// Grant is one person's access to one or more servers, until Expires.
type Grant struct {
	ID          string    `yaml:"id" json:"id"`
	User        string    `yaml:"user" json:"user"`   // Unix account name
	Email       string    `yaml:"email" json:"email"` // dashboard sign-in of the requester
	Servers     []string  `yaml:"servers" json:"servers"`
	Sudo        bool      `yaml:"sudo" json:"sudo"`
	Reason      string    `yaml:"reason" json:"reason"`
	PublicKey   string    `yaml:"public_key" json:"public_key"`
	RequestedAt time.Time `yaml:"requested_at" json:"requested_at"`
	Expires     time.Time `yaml:"expires" json:"expires"`
}

// File is access/grants.yaml.
type File struct {
	Grants []Grant `yaml:"grants" json:"grants"`
}

// Server is one entry of the policy's server list.
type Server struct {
	Host       string `yaml:"host" json:"host"`             // address reachable from the runner (WireGuard)
	SSHUser    string `yaml:"ssh_user" json:"-"`            // account the runner uses to apply changes
	Production bool   `yaml:"production" json:"production"` // production needs two approvals
}

// Admin can approve pull requests. Email links the GitHub account to the
// dashboard account, so an admin can never approve their own request.
type Admin struct {
	GitHub string `yaml:"github" json:"github"`
	Email  string `yaml:"email" json:"email"`
}

// Policy is access/policy.yaml.
type Policy struct {
	Servers       map[string]Server `yaml:"servers" json:"servers"`
	MaxDays       int               `yaml:"max_days" json:"max_days"`
	MaxSudoDays   int               `yaml:"max_sudo_days" json:"max_sudo_days"`
	Admins        []Admin           `yaml:"admins" json:"admins"`
	ReservedUsers []string          `yaml:"reserved_users" json:"-"`
}

func strictDecode(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) { // empty file = zero value
		return err
	}
	return nil
}

// ParseFile parses grants.yaml, rejecting unknown fields.
func ParseFile(data []byte) (*File, error) {
	var f File
	if err := strictDecode(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", GrantsPath, err)
	}
	if f.Grants == nil {
		f.Grants = []Grant{}
	}
	return &f, nil
}

// ParsePolicy parses policy.yaml and checks it is usable.
func ParsePolicy(data []byte) (*Policy, error) {
	var p Policy
	if err := strictDecode(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", PolicyPath, err)
	}
	switch {
	case len(p.Servers) == 0:
		return nil, fmt.Errorf("%s: no servers", PolicyPath)
	case p.MaxDays < 1 || p.MaxDays > 365:
		return nil, fmt.Errorf("%s: max_days must be 1..365", PolicyPath)
	case p.MaxSudoDays < 1 || p.MaxSudoDays > p.MaxDays:
		return nil, fmt.Errorf("%s: max_sudo_days must be 1..max_days", PolicyPath)
	case len(p.Admins) == 0:
		return nil, fmt.Errorf("%s: at least one admin is required", PolicyPath)
	}
	return &p, nil
}

const fileHeader = `# SSH access grants. Managed by the BM Monitor dashboard: every change arrives
# as a pull request, is validated by CI, and is applied by the access-apply job
# only after the required admin approvals. Do not edit by hand. See docs/ACCESS.md.
`

// Marshal renders the file in its canonical form (grants sorted by id), so
// diffs only ever show the entry that changed.
func (f *File) Marshal() ([]byte, error) {
	out := File{Grants: append([]Grant(nil), f.Grants...)}
	sort.Slice(out.Grants, func(i, j int) bool { return out.Grants[i].ID < out.Grants[j].ID })
	for i := range out.Grants {
		out.Grants[i].RequestedAt = out.Grants[i].RequestedAt.UTC()
		out.Grants[i].Expires = out.Grants[i].Expires.UTC()
	}
	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Find returns the grant with id, or nil.
func (f *File) Find(id string) *Grant {
	for i := range f.Grants {
		if f.Grants[i].ID == id {
			return &f.Grants[i]
		}
	}
	return nil
}

// Active reports whether g is in force at t.
func (g Grant) Active(t time.Time) bool { return !t.Before(g.RequestedAt) && t.Before(g.Expires) }

// NeedsTwoApprovals reports whether adding or changing g needs two admins:
// sudo, or any production server.
func (p *Policy) NeedsTwoApprovals(g Grant) bool {
	if g.Sudo {
		return true
	}
	for _, s := range g.Servers {
		if p.Servers[s].Production {
			return true
		}
	}
	return false
}

// AdminByGitHub returns the admin with that GitHub login, or nil.
func (p *Policy) AdminByGitHub(login string) *Admin {
	for i := range p.Admins {
		if strings.EqualFold(p.Admins[i].GitHub, login) {
			return &p.Admins[i]
		}
	}
	return nil
}
