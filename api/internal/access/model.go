// Package access implements the SSH access workflow: the shared grants file,
// the access policy, request validation, pull-request checks and SSH
// certificate signing. The same code runs in the dashboard (to build requests),
// in CI (to validate pull requests) and on the runner (to apply grants), so all
// three enforce identical rules.
package access

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
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

// Grant is one person's access to servers, until Expires. Servers can be named
// directly, or through access groups defined in the policy; a group grant
// follows the group, so servers added to or removed from it later are covered
// or dropped on the next apply.
type Grant struct {
	ID          string    `yaml:"id" json:"id"`
	User        string    `yaml:"user" json:"user"`   // Unix account name
	Email       string    `yaml:"email" json:"email"` // dashboard sign-in of the requester
	Servers     []string  `yaml:"servers,omitempty" json:"servers"`
	Groups      []string  `yaml:"groups,omitempty" json:"groups,omitempty"`
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
	Production bool   `yaml:"production" json:"production"` // production needs production_approvals
}

// Admin can approve pull requests. Email links the GitHub account to the
// dashboard account, so an admin can never approve their own request.
type Admin struct {
	GitHub string `yaml:"github" json:"github"`
	Email  string `yaml:"email" json:"email"`
}

// Policy is access/policy.yaml.
type Policy struct {
	Servers map[string]Server   `yaml:"servers" json:"servers"`
	Groups  map[string][]string `yaml:"groups" json:"groups"` // access group → server names
	MaxDays int                 `yaml:"max_days" json:"max_days"`
	// Admins who must approve: a request covering a production server needs
	// ProductionApprovals, a sudo request SudoApprovals, anything else one.
	// Both default to 2 when left out.
	ProductionApprovals int      `yaml:"production_approvals" json:"production_approvals"`
	SudoApprovals       int      `yaml:"sudo_approvals" json:"sudo_approvals"`
	MaxSudoDays         int      `yaml:"max_sudo_days" json:"max_sudo_days"`
	Admins              []Admin  `yaml:"admins" json:"admins"`
	ReservedUsers       []string `yaml:"reserved_users" json:"-"`
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
	for _, n := range []*int{&p.ProductionApprovals, &p.SudoApprovals} {
		if *n == 0 {
			*n = 2
		}
		if *n < 1 || *n > 5 {
			return nil, fmt.Errorf("%s: production_approvals and sudo_approvals must be 1..5", PolicyPath)
		}
	}
	for name, servers := range p.Groups {
		if !groupNameRe.MatchString(name) {
			return nil, fmt.Errorf("%s: group %q: use 1-64 letters, digits, spaces, dots, dashes or underscores", PolicyPath, name)
		}
		seen := map[string]bool{}
		for _, s := range servers {
			if _, ok := p.Servers[s]; !ok {
				return nil, fmt.Errorf("%s: group %q: unknown server %q", PolicyPath, name, s)
			}
			if seen[s] {
				return nil, fmt.Errorf("%s: group %q: %q listed twice", PolicyPath, name, s)
			}
			seen[s] = true
		}
	}
	return &p, nil
}

var groupNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$`)

// ServersOf is every server a grant covers under this policy: its named
// servers plus the current members of its groups, sorted and unique.
func (p *Policy) ServersOf(g Grant) []string {
	set := map[string]bool{}
	for _, s := range g.Servers {
		set[s] = true
	}
	for _, grp := range g.Groups {
		for _, s := range p.Groups[grp] {
			set[s] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// GroupNames returns the policy's access groups, sorted.
func (p *Policy) GroupNames() []string {
	out := make([]string, 0, len(p.Groups))
	for g := range p.Groups {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
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

// RequiredApprovals is how many admins must approve g: one, or the policy's
// production number if it covers a production server (directly or through a
// group), or its sudo number for sudo, whichever is highest.
func (p *Policy) RequiredApprovals(g Grant) int {
	n := 1
	if g.Sudo {
		n = max(n, p.SudoApprovals)
	}
	for _, s := range p.ServersOf(g) {
		if p.Servers[s].Production {
			n = max(n, p.ProductionApprovals)
		}
	}
	return n
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
