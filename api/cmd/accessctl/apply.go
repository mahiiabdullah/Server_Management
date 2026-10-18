package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"bm-monitor/api/internal/access"
	"bm-monitor/api/internal/ghclient"
)

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	grants := fs.String("grants", access.GrantsPath, "grants.yaml (from main)")
	pol := fs.String("policy", access.PolicyPath, "policy.yaml (from main)")
	stateDir := fs.String("state-dir", "state", "checkout of the access-state branch")
	caKey := fs.String("ca", "/etc/bm-access/ca", "CA private key (OpenSSH format)")
	caPassFile := fs.String("ca-passphrase-file", "", "file holding the CA key passphrase, if encrypted")
	deployKey := fs.String("deploy-key", "/etc/bm-access/deploy_ed25519", "key the runner uses to reach the servers")
	knownHosts := fs.String("known-hosts", "/etc/bm-access/known_hosts", "pinned host keys of the servers")
	commit := fs.String("commit", os.Getenv("GITHUB_SHA"), "commit being applied")
	dryRun := fs.Bool("dry-run", false, "sign and compute, but do not touch servers")
	verifyPol := fs.Bool("verify-policy", false, "require the policy to come from a merged PR approved by another admin; otherwise use the last approved policy")
	_ = fs.Parse(args)

	// Defence in depth: the runner must only ever act on main, whatever the workflow file says.
	if os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("GITHUB_REF") != "refs/heads/main" {
		return fmt.Errorf("refusing to apply from %s: only refs/heads/main may change servers", os.Getenv("GITHUB_REF"))
	}

	c, err := client()
	if err != nil {
		return err
	}
	p, note, err := choosePolicy(context.Background(), c, *pol, *stateDir, *verifyPol)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Println("::warning::" + note)
	}
	f, err := readFile(*grants)
	if err != nil {
		return err
	}
	if probs := p.ValidateFile(f); len(probs) > 0 {
		return fmt.Errorf("grants file is inconsistent:\n%s", probs.Error())
	}
	ca, err := loadCA(*caKey, *caPassFile)
	if err != nil {
		return err
	}
	st, certs, err := loadState(*stateDir)
	if err != nil {
		return err
	}
	res, err := access.Apply(context.Background(), access.ApplyInput{
		File: f, Policy: p, State: st, Certs: certs, CA: ca,
		Verifier: ghVerifier{c: c, policy: p},
		Remote:   sshRemote{key: *deployKey, knownHosts: *knownHosts},
		KRL:      sshKeygenKRL,
		Now:      time.Now(), Commit: *commit, DryRun: *dryRun, Note: note,
	})
	if err != nil {
		return err
	}
	if err := saveState(*stateDir, res); err != nil {
		return err
	}
	failed := 0
	for name, s := range res.Status.Servers {
		if !p.Servers[name].IsReady() {
			fmt.Printf("%-6s skipped: not ready (server setup not done)\n", name)
			continue
		}
		if s.OK {
			fmt.Printf("%-6s ok (%d managed accounts)\n", name, s.Accounts)
		} else {
			failed++
			fmt.Printf("::warning::%s: %s\n", name, s.Error)
		}
	}
	for id, g := range res.Status.Grants {
		if g.Status == "awaiting_approval" {
			fmt.Printf("::warning::grant %s not applied: %s\n", id, g.Reason)
		}
	}
	if failed > 0 && !*dryRun {
		return fmt.Errorf("%d server(s) could not be updated; the next run retries", failed)
	}
	return nil
}

const approvedPolicyFile = "policy.approved.yaml"

// choosePolicy returns the policy to apply. With verify, a policy on main that
// was not approved by another admin is ignored in favour of the last approved
// copy (kept on the access-state branch), so revocations keep working.
func choosePolicy(ctx context.Context, c *ghclient.Client, path, stateDir string, verify bool) (*access.Policy, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	p, err := access.ParsePolicy(data)
	if err != nil {
		return nil, "", err
	}
	if !verify {
		return p, "", nil
	}
	ok, reason, err := policyVerified(ctx, c, data)
	if err != nil {
		return nil, "", err
	}
	saved := filepath.Join(stateDir, approvedPolicyFile)
	if ok {
		if err := os.WriteFile(saved, data, 0o644); err != nil {
			return nil, "", err
		}
		return p, "", nil
	}
	old, err := os.ReadFile(saved)
	if err != nil {
		return nil, "", fmt.Errorf("the policy on main is not approved (%s) and no approved policy is known yet: merge a reviewed policy pull request first", reason)
	}
	op, err := access.ParsePolicy(old)
	if err != nil {
		return nil, "", fmt.Errorf("last approved policy: %w", err)
	}
	return op, "Policy change on main is not approved yet (" + reason + "); applying the last approved policy.", nil
}

func loadCA(path, passFile string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	if passFile != "" {
		pass, err := os.ReadFile(passFile)
		if err != nil {
			return nil, fmt.Errorf("CA passphrase: %w", err)
		}
		return ssh.ParsePrivateKeyWithPassphrase(pem, bytes.TrimSpace(pass))
	}
	return ssh.ParsePrivateKey(pem)
}

func loadState(dir string) (*access.State, map[string][]byte, error) {
	st := &access.State{}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, st); err != nil {
			return nil, nil, fmt.Errorf("state.json: %w", err)
		}
	}
	certs := map[string][]byte{}
	entries, _ := os.ReadDir(filepath.Join(dir, "certs"))
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), "-cert.pub"); ok {
			b, err := os.ReadFile(filepath.Join(dir, "certs", e.Name()))
			if err == nil {
				certs[id] = b
			}
		}
	}
	return st, certs, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func saveState(dir string, res *access.ApplyResult) error {
	if err := os.MkdirAll(filepath.Join(dir, "certs"), 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "state.json"), res.State); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "status.json"), res.Status); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "revoked.krl"), res.KRL, 0o644); err != nil {
		return err
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "certs"))
	for _, e := range entries { // drop certificates that are no longer active
		id, _ := strings.CutSuffix(e.Name(), "-cert.pub")
		if _, keep := res.Certs[id]; !keep {
			_ = os.Remove(filepath.Join(dir, "certs", e.Name()))
		}
	}
	for id, c := range res.Certs {
		if err := os.WriteFile(filepath.Join(dir, "certs", id+"-cert.pub"), c, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// sshKeygenKRL builds a key revocation list with ssh-keygen (Go has no KRL writer).
func sshKeygenKRL(caPub ssh.PublicKey, serials []uint64) ([]byte, error) {
	dir, err := os.MkdirTemp("", "bm-krl-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	caFile, spec, out := filepath.Join(dir, "ca.pub"), filepath.Join(dir, "spec"), filepath.Join(dir, "revoked.krl")
	if err := os.WriteFile(caFile, ssh.MarshalAuthorizedKey(caPub), 0o644); err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, s := range serials {
		fmt.Fprintf(&b, "serial: %d\n", s)
	}
	if err := os.WriteFile(spec, []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh-keygen", "-q", "-k", "-f", out, "-s", caFile, spec)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh-keygen -k: %v: %s", err, msg)
	}
	return os.ReadFile(out)
}

// sshRemote talks to the bm-access-apply forced command on each server through
// the deploy user. The protocol is line based (see deploy/ssh-access/bm-access-apply).
type sshRemote struct{ key, knownHosts string }

var resultLine = regexp.MustCompile(`^(OK|DISABLED|ERROR|KRL) `)

func (r sshRemote) Apply(ctx context.Context, name string, srv access.Server, accounts []access.Account, krl []byte) error {
	var in bytes.Buffer
	in.WriteString("BM-ACCESS 1\n")
	for _, a := range accounts {
		fmt.Fprintf(&in, "USER %s %d\n", a.User, map[bool]int{false: 0, true: 1}[a.Sudo])
	}
	fmt.Fprintf(&in, "KRL %s\nEND\n", base64.StdEncoding.EncodeToString(krl))

	user := srv.SSHUser
	if user == "" {
		user = "bm-access-deploy"
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-i", r.key, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+r.knownHosts, "-o", "ConnectTimeout=15",
		user+"@"+srv.Host, "bm-access-apply")
	cmd.Stdin = &in
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	runErr := cmd.Run()
	var errs []string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		line := sc.Text()
		if resultLine.MatchString(line) {
			fmt.Printf("  %s: %s\n", name, line)
		}
		if strings.HasPrefix(line, "ERROR ") {
			errs = append(errs, strings.TrimPrefix(line, "ERROR "))
		}
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		errs = append(errs, fmt.Sprintf("ssh: %v %s", runErr, msg))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
