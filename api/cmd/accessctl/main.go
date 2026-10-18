// Command accessctl runs the SSH access workflow's automated steps.
//
//	accessctl validate          CI: check a pull request's change to access/grants.yaml
//	accessctl approvals         CI: check a pull request has the approvals the policy needs
//	accessctl policy-approvals  CI: check a policy change is approved by another admin
//	accessctl check-policy      CI: check a changed policy against the current grants
//	accessctl apply             runner: sign certificates and reconcile accounts on the servers
//
// The same rules (package access) run in the dashboard, so a request that the
// dashboard accepts is judged identically here.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"bm-monitor/api/internal/access"
	"bm-monitor/api/internal/ghclient"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "approvals":
		err = cmdApprovals(os.Args[2:])
	case "apply":
		err = cmdApply(os.Args[2:])
	case "check-policy":
		err = cmdCheckPolicy(os.Args[2:])
	case "policy-approvals":
		err = cmdPolicyApprovals(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "accessctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: accessctl validate|approvals|policy-approvals|check-policy|apply [flags]")
	os.Exit(2)
}

func readFile(path string) (*access.File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &access.File{Grants: []access.Grant{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return access.ParseFile(data)
}

func readPolicy(path string) (*access.Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return access.ParsePolicy(data)
}

// githubOutput appends key=value lines for later workflow steps.
func githubOutput(kv map[string]string) {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for k, v := range kv {
		fmt.Fprintf(f, "%s=%s\n", k, v)
	}
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	base := fs.String("base", "", "grants.yaml from the target branch")
	head := fs.String("head", "", "grants.yaml from the pull request")
	pol := fs.String("policy", access.PolicyPath, "policy.yaml (from the target branch, never from the pull request)")
	kind := fs.String("kind", "", "request | revoke (from the pull request's label)")
	_ = fs.Parse(args)
	p, err := readPolicy(*pol)
	if err != nil {
		return err
	}
	b, err := readFile(*base)
	if err != nil {
		return fmt.Errorf("base: %w", err)
	}
	h, err := readFile(*head)
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	res := p.CheckPR(b, h, *kind, time.Now())
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	githubOutput(map[string]string{
		"grant_id": res.GrantID, "requester_email": res.RequesterEmail, "required_approvals": fmt.Sprint(res.RequiredApprovals),
	})
	if len(res.Problems) > 0 {
		for _, pr := range res.Problems {
			fmt.Printf("::error file=%s::%s\n", access.GrantsPath, pr.String())
		}
		return fmt.Errorf("%d problem(s) in the request", len(res.Problems))
	}
	fmt.Println("Request is valid.")
	return nil
}

// cmdCheckPolicy parses a policy and checks the current grants still satisfy it.
func cmdCheckPolicy(args []string) error {
	fs := flag.NewFlagSet("check-policy", flag.ExitOnError)
	pol := fs.String("policy", access.PolicyPath, "policy.yaml to check")
	grants := fs.String("grants", access.GrantsPath, "grants.yaml it must remain compatible with")
	_ = fs.Parse(args)
	p, err := readPolicy(*pol)
	if err != nil {
		return err
	}
	f, err := readFile(*grants)
	if err != nil {
		return err
	}
	probs := p.ValidateFile(f)
	for _, g := range f.Grants {
		probs = append(probs, p.ValidateGrant(g, time.Now(), false)...)
	}
	if len(probs) > 0 {
		for _, pr := range probs {
			fmt.Printf("::error file=%s::%s\n", access.PolicyPath, pr.String())
		}
		return fmt.Errorf("%d existing grant(s) conflict with this policy", len(probs))
	}
	fmt.Printf("Policy OK: %d server(s), %d group(s), %d admin(s), %d grant(s) checked.\n", len(p.Servers), len(p.Groups), len(p.Admins), len(f.Grants))
	for _, name := range p.GroupNames() {
		var grants []string
		for _, g := range f.Grants {
			for _, x := range g.Groups {
				if x == name {
					grants = append(grants, g.ID+" ("+g.User+")")
				}
			}
		}
		fmt.Printf("  group %-20s %s; grants using it: %s\n", name, strings.Join(p.Groups[name], ", "), orNone(grants))
	}
	return nil
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

// cmdPolicyApprovals checks that a pull request changing the policy has an
// approval from an admin (of the policy before the change) who is not its author.
func cmdPolicyApprovals(args []string) error {
	fs := flag.NewFlagSet("policy-approvals", flag.ExitOnError)
	pol := fs.String("policy", access.PolicyPath, "policy.yaml from the target branch (its admins decide)")
	pr := fs.Int("pr", 0, "pull request number")
	_ = fs.Parse(args)
	base, err := readPolicy(*pol)
	if err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	ctx := context.Background()
	info, err := c.GetPR(ctx, *pr)
	if err != nil {
		return err
	}
	reviews, err := c.Reviews(ctx, *pr)
	if err != nil {
		return err
	}
	n, ignored := access.PolicyApprovals(base, reviews, info.HeadSHA, info.Author, info.Body)
	for _, i := range ignored {
		fmt.Println("Not counted:", i)
	}
	fmt.Printf("Policy approvals: %d of 1 required (an admin other than the author, on the latest commit).\n", n)
	if n < 1 {
		return errors.New("a policy change needs an approval from another admin")
	}
	return nil
}

func client() (*ghclient.Client, error) {
	repo, tok := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || tok == "" {
		return nil, errors.New("GITHUB_REPOSITORY and GITHUB_TOKEN must be set")
	}
	return ghclient.New(os.Getenv("GITHUB_API_URL"), repo, ghclient.StaticToken(tok)), nil
}

func cmdApprovals(args []string) error {
	fs := flag.NewFlagSet("approvals", flag.ExitOnError)
	pol := fs.String("policy", access.PolicyPath, "policy.yaml from the target branch")
	pr := fs.Int("pr", 0, "pull request number")
	required := fs.Int("required", 1, "approvals required")
	requester := fs.String("requester", "", "requester email (their own approval never counts)")
	_ = fs.Parse(args)
	p, err := readPolicy(*pol)
	if err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	ctx := context.Background()
	info, err := c.GetPR(ctx, *pr)
	if err != nil {
		return err
	}
	reviews, err := c.Reviews(ctx, *pr)
	if err != nil {
		return err
	}
	n, ignored := p.CountApprovals(reviews, info.HeadSHA, *requester)
	for _, i := range ignored {
		fmt.Println("Not counted:", i)
	}
	fmt.Printf("Approvals: %d of %d required (admins only, on the latest commit, not the requester).\n", n, *required)
	if n < *required {
		return fmt.Errorf("needs %d more admin approval(s)", *required-n)
	}
	return nil
}

// ghVerifier accepts a grant only if a merged pull request from the branch the
// dashboard created for it (access/request-<id>) added exactly this grant and
// had enough admin approvals on its final commit.
type ghVerifier struct {
	c      *ghclient.Client
	policy *access.Policy
}

// Verify requires two approvals for sudo and one otherwise; production servers
// are then held back per server by the apply engine unless there were two.
func (v ghVerifier) Verify(ctx context.Context, g access.Grant) (bool, int, string, error) {
	prs, err := v.c.PRsForBranch(ctx, "access/request-"+g.ID, "closed")
	if err != nil {
		return false, 0, "", err
	}
	want := access.GrantHash(g)
	reason := "no merged pull request from access/request-" + g.ID
	for _, pr := range prs {
		if !pr.Merged || pr.BaseRef != "main" {
			continue
		}
		if !contains(pr.Labels, "access-request") {
			reason = fmt.Sprintf("PR #%d is not labelled access-request", pr.Number)
			continue
		}
		data, _, err := v.c.GetFile(ctx, access.GrantsPath, pr.HeadSHA)
		if err != nil {
			return false, 0, "", err
		}
		f, err := access.ParseFile(data)
		if err != nil {
			reason = fmt.Sprintf("PR #%d: %v", pr.Number, err)
			continue
		}
		pg := f.Find(g.ID)
		if pg == nil || access.GrantHash(*pg) != want {
			reason = fmt.Sprintf("grant differs from what PR #%d approved", pr.Number)
			continue
		}
		reviews, err := v.c.Reviews(ctx, pr.Number)
		if err != nil {
			return false, 0, "", err
		}
		required := 1
		if g.Sudo {
			required = 2
		}
		n, _ := v.policy.CountApprovals(reviews, pr.HeadSHA, g.Email)
		if n < required {
			reason = fmt.Sprintf("PR #%d has %d of %d required admin approvals", pr.Number, n, required)
			continue
		}
		return true, n, "", nil
	}
	return false, 0, reason, nil
}

// policyVerified reports whether the policy on main came from a merged pull
// request approved by an admin other than its author, with exactly this content.
func policyVerified(ctx context.Context, c *ghclient.Client, current []byte) (bool, string, error) {
	sha, err := c.LastCommitFor(ctx, access.PolicyPath, "main")
	if err != nil {
		return false, "", fmt.Errorf("find the last policy change: %w", err)
	}
	prs, err := c.PRsForCommit(ctx, sha)
	if err != nil {
		return false, "", err
	}
	reason := fmt.Sprintf("commit %.7s changed the policy without a pull request", sha)
	for _, pr := range prs {
		if !pr.Merged || pr.BaseRef != "main" {
			continue
		}
		head, _, err := c.GetFile(ctx, access.PolicyPath, pr.HeadSHA)
		if err != nil {
			return false, "", err
		}
		if !bytes.Equal(bytes.TrimSpace(head), bytes.TrimSpace(current)) {
			reason = fmt.Sprintf("the policy on main differs from what PR #%d changed", pr.Number)
			continue
		}
		baseData, _, err := c.GetFile(ctx, access.PolicyPath, pr.BaseSHA)
		if err != nil {
			reason = fmt.Sprintf("PR #%d: cannot read the policy before the change", pr.Number)
			continue
		}
		base, err := access.ParsePolicy(baseData)
		if err != nil {
			reason = fmt.Sprintf("PR #%d: the policy before the change is invalid", pr.Number)
			continue
		}
		reviews, err := c.Reviews(ctx, pr.Number)
		if err != nil {
			return false, "", err
		}
		if n, _ := access.PolicyApprovals(base, reviews, pr.HeadSHA, pr.Author, pr.Body); n < 1 {
			reason = fmt.Sprintf("PR #%d has no approval from an admin other than its author", pr.Number)
			continue
		}
		return true, "", nil
	}
	return false, reason, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
