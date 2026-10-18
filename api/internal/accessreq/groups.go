package accessreq

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"bm-monitor/api/internal/access"
	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/ghclient"
)

// Access groups live in access/policy.yaml, so every change to who a group
// grant reaches is a reviewed pull request. Dashboard groups are the source:
// an admin proposes "make the access group match this dashboard group", and the
// bot opens a policy pull request for another admin to approve.

// Proposal is a policy pull request that sets one access group.
type Proposal struct {
	ID             string    `json:"id"`
	GroupName      string    `json:"group_name"`
	Servers        []string  `json:"servers"` // nil: remove the group
	RequesterEmail string    `json:"requester_email"`
	Status         string    `json:"status"` // pr_open | merged | rejected
	PRNumber       *int      `json:"pr_number"`
	PRURL          *string   `json:"pr_url"`
	CreatedAt      time.Time `json:"created_at"`
}

const proposalCols = `id, group_name, servers, requester_email, status, pr_number, pr_url, created_at`

func scanProposal(row interface{ Scan(...any) error }) (*Proposal, error) {
	var p Proposal
	err := row.Scan(&p.ID, &p.GroupName, &p.Servers, &p.RequesterEmail, &p.Status, &p.PRNumber, &p.PRURL, &p.CreatedAt)
	return &p, err
}

// GroupAccess compares a dashboard group with its access group.
type GroupAccess struct {
	Enabled       bool      `json:"enabled"`
	InPolicy      bool      `json:"in_policy"`      // the access group exists
	PolicyServers []string  `json:"policy_servers"` // its servers now
	Wanted        []string  `json:"wanted"`         // the dashboard group's servers that the policy knows
	NotReady      []string  `json:"not_ready"`      // dashboard servers not in the policy (server setup not done)
	Production    []string  `json:"production"`     // production servers among wanted
	InSync        bool      `json:"in_sync"`
	Grants        []string  `json:"grants"` // grants (id and user) that use the group
	Proposal      *Proposal `json:"proposal"`
	NameAllowed   bool      `json:"name_allowed"`
}

// GroupAccess reports how the access group compares with the dashboard group's servers.
func (s *Service) GroupAccess(ctx context.Context, name string, dashboardServers []string) (*GroupAccess, error) {
	if !s.Enabled() {
		return &GroupAccess{}, nil
	}
	p, err := s.Policy(ctx)
	if err != nil {
		return nil, err
	}
	out := &GroupAccess{Enabled: true, PolicyServers: []string{}, Wanted: []string{}, NotReady: []string{}, Production: []string{}, Grants: []string{}}
	out.NameAllowed = policyNameOK(name)
	cur, ok := p.Groups[name]
	out.InPolicy = ok
	out.PolicyServers = append(out.PolicyServers, cur...)
	sort.Strings(out.PolicyServers)
	for _, srv := range dashboardServers {
		if def, ok := p.Servers[srv]; ok {
			out.Wanted = append(out.Wanted, srv)
			if def.Production {
				out.Production = append(out.Production, srv)
			}
		} else {
			out.NotReady = append(out.NotReady, srv)
		}
	}
	sort.Strings(out.Wanted)
	sort.Strings(out.NotReady)
	out.InSync = ok && strings.Join(out.Wanted, ",") == strings.Join(out.PolicyServers, ",")
	if f, err := s.grantsOnMain(ctx); err == nil {
		for _, g := range f.Grants {
			for _, x := range g.Groups {
				if x == name {
					out.Grants = append(out.Grants, g.ID+" "+g.User)
				}
			}
		}
	}
	pr, err := scanProposal(s.pool.QueryRow(ctx, `SELECT `+proposalCols+` FROM access_policy_proposals
		WHERE group_name = $1 ORDER BY created_at DESC LIMIT 1`, name))
	if err == nil {
		out.Proposal = pr
	}
	return out, nil
}

func policyNameOK(name string) bool {
	_, err := access.SetPolicyGroup([]byte("servers: {x: {host: h}}\nmax_days: 1\nmax_sudo_days: 1\nadmins: [{github: a, email: a@b.c}]\n"), name, []string{})
	return err == nil
}

func (s *Service) grantsOnMain(ctx context.Context) (*access.File, error) {
	data, _, err := s.gh.GetFile(ctx, access.GrantsPath, MainBranch)
	if err != nil && !errors.Is(err, ghclient.ErrNotFound) {
		return nil, err
	}
	return access.ParseFile(data)
}

// ProposeGroup opens a policy pull request that makes access group name cover
// the dashboard group's servers (those the policy knows). Admin only.
func (s *Service) ProposeGroup(ctx context.Context, userID, email, name string, dashboardServers []string, ev audit.Event) (*Proposal, error) {
	ga, err := s.GroupAccess(ctx, name, dashboardServers)
	if err != nil {
		return nil, err
	}
	if !ga.Enabled {
		return nil, ErrNotConfigured
	}
	if !ga.NameAllowed {
		return nil, &ValidationError{Fields: map[string]string{"name": "Access group names use 1-64 letters, digits, spaces, dots, dashes or underscores. Rename the group first."}}
	}
	if ga.InSync {
		return nil, &ConflictError{"The access group already matches this group's servers."}
	}
	if ga.Proposal != nil && ga.Proposal.Status == "pr_open" {
		return nil, &ConflictError{fmt.Sprintf("A change for this group is already waiting for approval (PR #%d).", deref(ga.Proposal.PRNumber))}
	}
	s.createMux.Lock()
	defer s.createMux.Unlock()

	mainSHA, err := s.gh.BranchSHA(ctx, MainBranch)
	if err != nil {
		return nil, fmt.Errorf("read main: %w", err)
	}
	data, blobSHA, err := s.gh.GetFile(ctx, access.PolicyPath, mainSHA)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", access.PolicyPath, err)
	}
	content, err := access.SetPolicyGroup(data, name, ga.Wanted)
	if err != nil {
		return nil, &ValidationError{Fields: map[string]string{"name": err.Error()}}
	}
	branch := fmt.Sprintf("access/policy-group-%s-%d", slug(name), s.now().Unix())
	if err := s.gh.CreateBranch(ctx, branch, mainSHA); err != nil {
		return nil, fmt.Errorf("create branch: %w", err)
	}
	msg := fmt.Sprintf("Access group %s: %s", name, orNone(ga.Wanted))
	if err := s.gh.PutFile(ctx, access.PolicyPath, branch, msg, content, blobSHA); err != nil {
		return nil, fmt.Errorf("commit policy change: %w", err)
	}
	pr, err := s.gh.CreatePR(ctx, "SSH access group "+name+": "+orNone(ga.Wanted), proposalBody(name, ga, email), branch, MainBranch)
	if err != nil {
		return nil, fmt.Errorf("open pull request: %w", err)
	}
	if err := s.gh.AddLabels(ctx, pr.Number, "access-policy"); err != nil {
		s.log.Warn("label pull request", "pr", pr.Number, "err", err)
	}
	prop, err := scanProposal(s.pool.QueryRow(ctx, `INSERT INTO access_policy_proposals (group_name, servers, user_id, requester_email, branch, pr_number, pr_url, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'pr_open') RETURNING `+proposalCols, name, ga.Wanted, userID, strings.ToLower(email), branch, pr.Number, pr.HTMLURL))
	if err != nil {
		return nil, err
	}
	ev.Action, ev.TargetType, ev.TargetID = "access.group_proposed", "access_group", name
	ev.Before, ev.After = map[string]any{"servers": ga.PolicyServers}, map[string]any{"servers": ga.Wanted, "pr": pr.Number}
	_ = audit.Record(ctx, s.pool, ev)
	return prop, nil
}

func proposalBody(name string, ga *GroupAccess, email string) string {
	var added, removed []string
	cur := map[string]bool{}
	for _, x := range ga.PolicyServers {
		cur[x] = true
	}
	want := map[string]bool{}
	for _, x := range ga.Wanted {
		want[x] = true
		if !cur[x] {
			added = append(added, x)
		}
	}
	for _, x := range ga.PolicyServers {
		if !want[x] {
			removed = append(removed, x)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sets SSH access group **%s** in `%s`, to match the dashboard group of the same name.\n\n", name, access.PolicyPath)
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Servers after | %s |\n| Added | %s |\n| Removed | %s |\n", orNone(ga.Wanted), orNone(added), orNone(removed))
	if len(ga.Production) > 0 {
		fmt.Fprintf(&b, "| Production | %s (grants approved by one admin are not applied there) |\n", strings.Join(ga.Production, ", "))
	}
	if len(ga.NotReady) > 0 {
		fmt.Fprintf(&b, "| Not included | %s: not in the policy's server list yet (server setup not done) |\n", strings.Join(ga.NotReady, ", "))
	}
	fmt.Fprintf(&b, "| Grants affected | %s |\n\n", orNone(ga.Grants))
	b.WriteString("Everyone with a grant for this group gains access to the added servers and loses it on the removed ones at the next apply.\n\n")
	b.WriteString("**Approval:** an admin other than the requester. With the policy check on, the apply job ignores this change until then.\n\n")
	fmt.Fprintf(&b, "Requested by: %s\n", strings.ToLower(email))
	return b.String()
}

// syncProposals follows open policy pull requests to merged or closed.
func (s *Service) syncProposals(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT `+proposalCols+` FROM access_policy_proposals WHERE status = 'pr_open' AND pr_number IS NOT NULL`)
	if err != nil {
		return err
	}
	var open []*Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open = append(open, p)
	}
	rows.Close()
	for _, p := range open {
		pr, err := s.gh.GetPR(ctx, *p.PRNumber)
		if err != nil {
			s.log.Warn("read policy pull request", "pr", *p.PRNumber, "err", err)
			continue
		}
		status := ""
		switch {
		case pr.Merged:
			status = "merged"
			s.mu.Lock()
			s.policy = nil // read the new policy on next use
			s.mu.Unlock()
		case pr.State == "closed":
			status = "rejected"
		default:
			continue
		}
		if _, err := s.pool.Exec(ctx, `UPDATE access_policy_proposals SET status = $2 WHERE id = $1`, p.ID, status); err != nil {
			return err
		}
	}
	return nil
}

// ConfigGroup is an access group as the request form shows it.
type ConfigGroup struct {
	Name       string   `json:"name"`
	Servers    []string `json:"servers"`
	Production bool     `json:"production"`
}

// ConfigGroups lists the policy's access groups for the request form.
func ConfigGroups(p *access.Policy) []ConfigGroup {
	out := []ConfigGroup{}
	for _, name := range p.GroupNames() {
		g := ConfigGroup{Name: name, Servers: append([]string{}, p.Groups[name]...)}
		sort.Strings(g.Servers)
		for _, srv := range g.Servers {
			g.Production = g.Production || p.Servers[srv].Production
		}
		out = append(out, g)
	}
	return out
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
