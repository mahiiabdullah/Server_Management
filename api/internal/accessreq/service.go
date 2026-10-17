// Package accessreq is the dashboard side of the SSH access workflow: it turns
// a request from a signed-in user into a pull request that adds one entry to
// access/grants.yaml, tracks that pull request, and shows the result (status
// and certificate) published by the apply job on the access-state branch.
package accessreq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bm-monitor/api/internal/access"
	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/ghclient"
)

const (
	MainBranch  = "main"
	StateBranch = "access-state"
)

var (
	ErrNotConfigured = errors.New("the SSH access workflow is not configured (GITHUB_REPO)")
	ErrNotFound      = errors.New("not found")
)

// ValidationError lists problems with a request, per field.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string { return "invalid access request" }

// ConflictError means the request cannot be made right now.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

type Service struct {
	pool *pgxpool.Pool
	gh   *ghclient.Client
	log  *slog.Logger
	now  func() time.Time

	mu        sync.Mutex
	policy    *access.Policy
	policyAt  time.Time
	status    *access.Status
	statusAt  time.Time
	createMux sync.Mutex // one pull request at a time, so grant ids and file edits never race
}

func New(pool *pgxpool.Pool, gh *ghclient.Client, log *slog.Logger) *Service {
	return &Service{pool: pool, gh: gh, log: log, now: time.Now}
}

func (s *Service) Enabled() bool { return s != nil && s.gh != nil }
func (s *Service) Repo() string  { return s.gh.Repo() }

// Policy reads access/policy.yaml from main, cached for a minute.
func (s *Service) Policy(ctx context.Context) (*access.Policy, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policy != nil && time.Since(s.policyAt) < time.Minute {
		return s.policy, nil
	}
	data, _, err := s.gh.GetFile(ctx, access.PolicyPath, MainBranch)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", access.PolicyPath, err)
	}
	p, err := access.ParsePolicy(data)
	if err != nil {
		return nil, err
	}
	s.policy, s.policyAt = p, time.Now()
	return p, nil
}

// Request is one row of access_requests as the dashboard shows it.
type Request struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	GrantID        string     `json:"grant_id"`
	RequesterEmail string     `json:"requester_email"`
	UnixUser       string     `json:"unix_user"`
	Servers        []string   `json:"servers"`
	Sudo           bool       `json:"sudo"`
	Reason         string     `json:"reason"`
	KeyFingerprint *string    `json:"key_fingerprint"`
	RequestedAt    time.Time  `json:"requested_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Status         string     `json:"status"`
	StatusDetail   *string    `json:"status_detail"`
	PRNumber       *int       `json:"pr_number"`
	PRURL          *string    `json:"pr_url"`
	Certificate    *string    `json:"certificate,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

const cols = `id, kind, grant_id, requester_email, unix_user, servers, sudo, reason, key_fingerprint,
	requested_at, expires_at, status, status_detail, pr_number, pr_url, certificate, created_at`

func scan(row interface{ Scan(...any) error }) (*Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.Kind, &r.GrantID, &r.RequesterEmail, &r.UnixUser, &r.Servers, &r.Sudo, &r.Reason, &r.KeyFingerprint,
		&r.RequestedAt, &r.ExpiresAt, &r.Status, &r.StatusDetail, &r.PRNumber, &r.PRURL, &r.Certificate, &r.CreatedAt)
	return &r, err
}

// Input is what the user fills in.
type Input struct {
	Servers   []string
	Sudo      bool
	Days      int
	Reason    string
	PublicKey string
}

// Create validates a request with the same rules CI uses, then opens the pull request.
func (s *Service) Create(ctx context.Context, userID, email string, in Input, ev audit.Event) (*Request, error) {
	p, err := s.Policy(ctx)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	user, err := access.UsernameFromEmail(email)
	if err != nil {
		return nil, &ValidationError{Fields: map[string]string{"email": err.Error()}}
	}
	key, err := access.ParsePublicKey(in.PublicKey)
	if err != nil {
		fields["public_key"] = err.Error()
	}
	limit := p.MaxDays
	if in.Sudo {
		limit = p.MaxSudoDays
	}
	if in.Days < 1 || in.Days > limit {
		fields["days"] = fmt.Sprintf("Choose 1 to %d days%s.", limit, map[bool]string{true: " (sudo)", false: ""}[in.Sudo])
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	var open int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM access_requests WHERE kind = 'request' AND lower(requester_email) = lower($1)
		AND status IN ('pr_open','merged')`, email).Scan(&open); err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, &ConflictError{"You already have a request in progress (waiting for approval or for the apply job). Wait for it, or ask an admin to close it."}
	}

	// Validate the grant before allocating its number, so rejected attempts use none.
	now := s.now().UTC().Truncate(time.Second)
	g := access.Grant{ID: "0000", User: user, Email: strings.ToLower(email), Servers: in.Servers, Sudo: in.Sudo,
		Reason: strings.TrimSpace(in.Reason), PublicKey: key.Normalized, RequestedAt: now, Expires: now.AddDate(0, 0, in.Days)}
	if probs := p.ValidateGrant(g, now, true); len(probs) > 0 {
		return nil, &ValidationError{Fields: problemFields(probs)}
	}

	s.createMux.Lock()
	defer s.createMux.Unlock()

	mainSHA, err := s.gh.BranchSHA(ctx, MainBranch)
	if err != nil {
		return nil, fmt.Errorf("read main: %w", err)
	}
	data, blobSHA, err := s.gh.GetFile(ctx, access.GrantsPath, mainSHA)
	if err != nil && !errors.Is(err, ghclient.ErrNotFound) {
		return nil, fmt.Errorf("read %s: %w", access.GrantsPath, err)
	}
	f, err := access.ParseFile(data)
	if err != nil {
		return nil, err
	}

	var id string
	for {
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT nextval('access_grant_seq')`).Scan(&n); err != nil {
			return nil, err
		}
		id = fmt.Sprintf("%04d", n)
		if f.Find(id) == nil {
			break
		}
	}
	g.ID = id
	head := &access.File{Grants: append(append([]access.Grant(nil), f.Grants...), g)}
	check := p.CheckPR(f, head, access.KindRequest, now)
	if len(check.Problems) > 0 {
		return nil, &ValidationError{Fields: problemFields(check.Problems)}
	}
	content, err := head.Marshal()
	if err != nil {
		return nil, err
	}

	branch := "access/request-" + id
	if err := s.gh.CreateBranch(ctx, branch, mainSHA); err != nil {
		return nil, fmt.Errorf("create branch: %w", err)
	}
	if err := s.gh.PutFile(ctx, access.GrantsPath, branch, fmt.Sprintf("Access request %s: %s on %s", id, user, strings.Join(in.Servers, ", ")), content, blobSHA); err != nil {
		return nil, fmt.Errorf("commit request: %w", err)
	}
	pr, err := s.gh.CreatePR(ctx, prTitle(g), prBody(g, key, p, check.RequiredApprovals), branch, MainBranch)
	if err != nil {
		return nil, fmt.Errorf("open pull request: %w", err)
	}
	if err := s.gh.AddLabels(ctx, pr.Number, "access-request"); err != nil {
		s.log.Warn("label pull request", "pr", pr.Number, "err", err)
	}

	r, err := scan(s.pool.QueryRow(ctx, `INSERT INTO access_requests (kind, grant_id, user_id, requester_email, unix_user, servers, sudo, reason,
		key_fingerprint, requested_at, expires_at, status, branch, pr_number, pr_url)
		VALUES ('request',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pr_open',$11,$12,$13) RETURNING `+cols,
		id, userID, g.Email, user, g.Servers, g.Sudo, g.Reason, key.Fingerprint, g.RequestedAt, g.Expires, branch, pr.Number, pr.HTMLURL))
	if err != nil {
		return nil, err
	}
	ev.Action, ev.TargetType, ev.TargetID = "access.request", "access_grant", id
	ev.After = map[string]any{"servers": g.Servers, "sudo": g.Sudo, "days": in.Days, "pr": pr.Number, "key": key.Fingerprint}
	_ = audit.Record(ctx, s.pool, ev)
	return r, nil
}

// problemFields maps rule violations onto the request form's fields.
func problemFields(ps access.Problems) map[string]string {
	fe := map[string]string{}
	for _, pr := range ps {
		field := map[string]string{"servers": "servers", "reason": "reason", "public_key": "public_key", "expires": "days"}[pr.Field]
		if field == "" {
			field = "form"
		}
		fe[field] = pr.Message
	}
	return fe
}

func prTitle(g access.Grant) string {
	sudo := ""
	if g.Sudo {
		sudo = " with sudo"
	}
	return fmt.Sprintf("SSH access: %s → %s%s until %s", g.User, strings.Join(g.Servers, ", "), sudo, g.Expires.Format("2 Jan 2006"))
}

func prBody(g access.Grant, key *access.KeyInfo, p *access.Policy, required int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Access request **%s**, opened from the BM Monitor dashboard.\n\n", g.ID)
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Requested by | %s |\n| Unix account | `%s` |\n| Servers | %s |\n", g.Email, g.User, strings.Join(g.Servers, ", "))
	fmt.Fprintf(&b, "| Sudo | %s |\n| Valid | %s → %s (UTC) |\n", map[bool]string{true: "**yes**", false: "no"}[g.Sudo],
		g.RequestedAt.Format("2006-01-02 15:04"), g.Expires.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "| Key | `%s` (%s) |\n| Reason | %s |\n\n", key.Fingerprint, key.Type, strings.ReplaceAll(g.Reason, "|", "/"))
	fmt.Fprintf(&b, "**Approvals required: %d** from admins in `access/policy.yaml`, on the latest commit. ", required)
	b.WriteString("The requester's own approval never counts.\n\n")
	b.WriteString("Before approving, check that the person and the reason are expected. ")
	b.WriteString("After merge, the apply job verifies these approvals again before it grants anything.\n")
	return b.String()
}

// Revoke opens a pull request that removes one grant. Admin only.
func (s *Service) Revoke(ctx context.Context, adminID, grantID string, ev audit.Event) (*Request, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	s.createMux.Lock()
	defer s.createMux.Unlock()
	mainSHA, err := s.gh.BranchSHA(ctx, MainBranch)
	if err != nil {
		return nil, err
	}
	data, blobSHA, err := s.gh.GetFile(ctx, access.GrantsPath, mainSHA)
	if err != nil {
		return nil, err
	}
	f, err := access.ParseFile(data)
	if err != nil {
		return nil, err
	}
	g := f.Find(grantID)
	if g == nil {
		return nil, ErrNotFound
	}
	var pending int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM access_requests WHERE kind = 'revoke' AND grant_id = $1 AND status IN ('pr_open','merged')`,
		grantID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, &ConflictError{"A revoke for this grant is already waiting for approval."}
	}
	head := &access.File{}
	for _, x := range f.Grants {
		if x.ID != grantID {
			head.Grants = append(head.Grants, x)
		}
	}
	content, err := head.Marshal()
	if err != nil {
		return nil, err
	}
	branch := fmt.Sprintf("access/revoke-%s-%d", grantID, s.now().Unix())
	if err := s.gh.CreateBranch(ctx, branch, mainSHA); err != nil {
		return nil, err
	}
	if err := s.gh.PutFile(ctx, access.GrantsPath, branch, "Revoke access grant "+grantID, content, blobSHA); err != nil {
		return nil, err
	}
	body := fmt.Sprintf("Revokes grant **%s**: `%s` on %s (requested by %s).\n\nOpened from the BM Monitor dashboard. "+
		"After merge, the apply job removes the account and adds the certificate to the revocation list.\n", g.ID, g.User, strings.Join(g.Servers, ", "), g.Email)
	pr, err := s.gh.CreatePR(ctx, fmt.Sprintf("Revoke SSH access: %s (grant %s)", g.User, g.ID), body, branch, MainBranch)
	if err != nil {
		return nil, err
	}
	if err := s.gh.AddLabels(ctx, pr.Number, "access-revoke"); err != nil {
		s.log.Warn("label pull request", "pr", pr.Number, "err", err)
	}
	r, err := scan(s.pool.QueryRow(ctx, `INSERT INTO access_requests (kind, grant_id, user_id, requester_email, unix_user, servers, sudo, reason,
		requested_at, expires_at, status, branch, pr_number, pr_url)
		VALUES ('revoke',$1,$2,$3,$4,$5,$6,$7,$8,$9,'pr_open',$10,$11,$12)
		ON CONFLICT (kind, grant_id) DO UPDATE SET status = 'pr_open', branch = EXCLUDED.branch, pr_number = EXCLUDED.pr_number,
		  pr_url = EXCLUDED.pr_url, user_id = EXCLUDED.user_id, status_detail = NULL
		RETURNING `+cols,
		g.ID, adminID, g.Email, g.User, g.Servers, g.Sudo, "Revoked by an admin", s.now().UTC(), g.Expires, branch, pr.Number, pr.HTMLURL))
	if err != nil {
		return nil, err
	}
	ev.Action, ev.TargetType, ev.TargetID, ev.After = "access.revoke_requested", "access_grant", grantID, map[string]any{"pr": pr.Number}
	_ = audit.Record(ctx, s.pool, ev)
	return r, nil
}

// List returns requests: the user's own, or everyone's for an admin.
func (s *Service) List(ctx context.Context, email string, all bool) ([]Request, error) {
	q := `SELECT ` + cols + ` FROM access_requests WHERE ($2 OR lower(requester_email) = lower($1)) ORDER BY created_at DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, q, email, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if all && !strings.EqualFold(r.RequesterEmail, email) {
			r.Certificate = nil // a certificate is shown to its owner only
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GrantView is one grant on main with its applied status.
type GrantView struct {
	access.Grant
	Status      string            `json:"status"`
	Reason      string            `json:"status_reason,omitempty"`
	ServerState map[string]string `json:"server_state,omitempty"`
}

// Grants returns everything in grants.yaml on main with the latest applied status. Admin only.
func (s *Service) Grants(ctx context.Context) ([]GrantView, *access.Status, error) {
	if !s.Enabled() {
		return nil, nil, ErrNotConfigured
	}
	data, _, err := s.gh.GetFile(ctx, access.GrantsPath, MainBranch)
	if err != nil && !errors.Is(err, ghclient.ErrNotFound) {
		return nil, nil, err
	}
	f, err := access.ParseFile(data)
	if err != nil {
		return nil, nil, err
	}
	st, _ := s.appliedStatus(ctx, true)
	out := []GrantView{}
	for _, g := range f.Grants {
		v := GrantView{Grant: g, Status: "not_applied_yet"}
		if st != nil {
			if gs, ok := st.Grants[g.ID]; ok {
				v.Status, v.Reason, v.ServerState = gs.Status, gs.Reason, gs.Servers
			}
		}
		out = append(out, v)
	}
	return out, st, nil
}

// appliedStatus reads status.json from the access-state branch, cached for 30 s.
func (s *Service) appliedStatus(ctx context.Context, fresh bool) (*access.Status, error) {
	s.mu.Lock()
	if !fresh && s.status != nil && time.Since(s.statusAt) < 30*time.Second {
		defer s.mu.Unlock()
		return s.status, nil
	}
	s.mu.Unlock()
	data, _, err := s.gh.GetFile(ctx, "status.json", StateBranch)
	if errors.Is(err, ghclient.ErrNotFound) {
		return nil, nil // the apply job has not run yet
	}
	if err != nil {
		return nil, err
	}
	var st access.Status
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("status.json: %w", err)
	}
	s.mu.Lock()
	s.status, s.statusAt = &st, time.Now()
	s.mu.Unlock()
	return &st, nil
}

// Sync advances open requests: pull request merged or closed, grant applied,
// expired or revoked. Runs every minute.
func (s *Service) Sync(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM access_requests WHERE status IN ('pr_open','merged','active')`)
	if err != nil {
		return err
	}
	var open []*Request
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open = append(open, r)
	}
	rows.Close()
	if len(open) == 0 {
		return nil
	}
	st, err := s.appliedStatus(ctx, true)
	if err != nil {
		s.log.Warn("read access-state", "err", err)
	}
	now := s.now()
	for _, r := range open {
		status, detail := r.Status, ""
		if r.StatusDetail != nil {
			detail = *r.StatusDetail
		}
		var cert *string
		// A merged request is re-checked until it is applied: on the free plan GitHub
		// lets anyone with write access merge without approvals.
		if r.PRNumber != nil && (r.Status == "pr_open" || (r.Status == "merged" && r.Kind == "request")) {
			pr, err := s.gh.GetPR(ctx, *r.PRNumber)
			if err != nil {
				s.log.Warn("read pull request", "pr", *r.PRNumber, "err", err)
				continue
			}
			switch {
			case pr.Merged && r.Kind == "request":
				short, err := s.approvalShortfall(ctx, r, pr)
				if err != nil {
					s.log.Warn("count approvals", "pr", *r.PRNumber, "err", err)
					continue
				}
				status, detail = "merged", "Approved and merged; waiting for the apply job."
				if short != "" {
					status, detail = "failed", short
				}
			case pr.Merged:
				status, detail = "merged", "Approved and merged; waiting for the apply job."
			case pr.State == "closed":
				status, detail = "rejected", "The pull request was closed without merging."
			}
		}
		if status == "merged" || status == "active" {
			var gs *access.GrantStatus
			if st != nil {
				if x, ok := st.Grants[r.GrantID]; ok {
					gs = &x
				}
			}
			// Only trust a status published after this request was made.
			fresh := st != nil && st.GeneratedAt.After(r.CreatedAt)
			switch {
			case r.Kind == "revoke" && fresh && (gs == nil || gs.Status != "active"):
				status, detail = "revoked", "Applied: access removed and certificate revoked."
				if _, err := s.pool.Exec(ctx, `UPDATE access_requests SET status = 'revoked', status_detail = 'Revoked by an admin.'
					WHERE kind = 'request' AND grant_id = $1 AND status <> 'revoked'`, r.GrantID); err != nil {
					return err
				}
			case r.Kind == "request" && gs != nil && gs.Status == "active":
				if status != "active" || r.Certificate == nil {
					if c, _, err := s.gh.GetFile(ctx, "certs/"+r.GrantID+"-cert.pub", StateBranch); err == nil {
						v := strings.TrimSpace(string(c))
						cert = &v
					}
				}
				status, detail = "active", applyDetail(gs)
			case r.Kind == "request" && gs != nil && gs.Status == "expired",
				r.Kind == "request" && r.ExpiresAt != nil && !now.Before(*r.ExpiresAt):
				status, detail = "expired", "Access ended on its expiry date."
			case r.Kind == "request" && gs != nil && gs.Status == "awaiting_approval":
				detail = "Not applied: " + gs.Reason
			case r.Kind == "request" && status == "active" && fresh && gs == nil:
				status, detail = "revoked", "The grant was removed from access/grants.yaml."
			}
		}
		if status != r.Status || detail != strPtrVal(r.StatusDetail) || cert != nil {
			if _, err := s.pool.Exec(ctx, `UPDATE access_requests SET status = $2, status_detail = $3, certificate = coalesce($4, certificate) WHERE id = $1`,
				r.ID, status, detail, cert); err != nil {
				return err
			}
		}
	}
	return nil
}

// approvalShortfall applies the same approval rule as the apply job to a merged
// request. It returns why the request will not be applied, or "" when it will.
// Reviews cannot be added after a merge, so a shortfall is final.
func (s *Service) approvalShortfall(ctx context.Context, r *Request, pr *ghclient.PR) (string, error) {
	p, err := s.Policy(ctx)
	if err != nil {
		return "", err
	}
	reviews, err := s.gh.Reviews(ctx, pr.Number)
	if err != nil {
		return "", err
	}
	need := 1
	if p.NeedsTwoApprovals(access.Grant{Servers: r.Servers, Sudo: r.Sudo}) {
		need = 2
	}
	n, ignored := p.CountApprovals(reviews, pr.HeadSHA, r.RequesterEmail)
	if n >= need {
		return "", nil
	}
	msg := fmt.Sprintf("Merged without the required approvals (%d of %d), so it will not be applied.", n, need)
	if len(ignored) > 0 {
		msg += " Not counted: " + strings.Join(ignored, ", ") + "."
	}
	return msg + " An admin should revoke grant " + r.GrantID + "; then you can request again.", nil
}

func applyDetail(gs *access.GrantStatus) string {
	var bad []string
	for srv, st := range gs.Servers {
		if st != "ok" {
			bad = append(bad, srv+": "+st)
		}
	}
	if len(bad) > 0 {
		return "Certificate issued; not yet applied on " + strings.Join(bad, "; ")
	}
	return "Applied on all requested servers."
}

func strPtrVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Run syncs every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("access sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
