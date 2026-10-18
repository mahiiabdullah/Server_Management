// Package ghclient is a minimal GitHub REST client for the SSH access workflow:
// read and write files on branches, open pull requests, read reviews. It
// authenticates with a token or as a GitHub App installation.
package ghclient

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bm-monitor/api/internal/access"
)

var ErrNotFound = errors.New("not found on GitHub")

// TokenSource returns a bearer token for API calls.
type TokenSource func(ctx context.Context) (string, error)

// StaticToken uses a fixed token (a fine-grained personal access token, or the
// GITHUB_TOKEN of a workflow run).
func StaticToken(tok string) TokenSource {
	return func(context.Context) (string, error) { return tok, nil }
}

type Client struct {
	base  string // https://api.github.com
	repo  string // owner/name
	token TokenSource
	http  *http.Client
}

func New(baseURL, repo string, token TokenSource) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), repo: repo, token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) Repo() string { return c.repo }

// APIError is a non-2xx answer from GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub API %d: %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	tok, err := c.token(ctx)
	if err != nil {
		return fmt.Errorf("GitHub token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &m)
		return &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) repoPath(format string, args ...any) string {
	return "/repos/" + c.repo + fmt.Sprintf(format, args...)
}

// GetFile returns a file's content and blob sha at ref (branch, tag or commit).
func (c *Client) GetFile(ctx context.Context, path, ref string) ([]byte, string, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/contents/%s?ref=%s", path, url.QueryEscape(ref)), nil, &out); err != nil {
		return nil, "", err
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", path, err)
	}
	return data, out.SHA, nil
}

// BranchSHA returns the commit a branch points to.
func (c *Client) BranchSHA(ctx context.Context, branch string) (string, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/git/ref/heads/%s", branch), nil, &out); err != nil {
		return "", err
	}
	return out.Object.SHA, nil
}

// CreateBranch creates branch at commit sha.
func (c *Client) CreateBranch(ctx context.Context, branch, sha string) error {
	return c.do(ctx, http.MethodPost, c.repoPath("/git/refs"), map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil)
}

// PutFile creates or updates a file on a branch. blobSHA is the current blob
// sha ("" to create); GitHub rejects the write if the file changed meanwhile.
func (c *Client) PutFile(ctx context.Context, path, branch, message string, content []byte, blobSHA string) error {
	body := map[string]any{"message": message, "content": base64.StdEncoding.EncodeToString(content), "branch": branch}
	if blobSHA != "" {
		body["sha"] = blobSHA
	}
	return c.do(ctx, http.MethodPut, c.repoPath("/contents/%s", path), body, nil)
}

// PR is the part of a pull request the workflow uses.
type PR struct {
	Number  int      `json:"number"`
	State   string   `json:"state"` // open | closed
	Merged  bool     `json:"merged"`
	HTMLURL string   `json:"html_url"`
	Body    string   `json:"body"`
	HeadSHA string   `json:"-"`
	HeadRef string   `json:"-"`
	BaseRef string   `json:"-"`
	BaseSHA string   `json:"-"`
	Author  string   `json:"-"` // login of whoever opened it
	Labels  []string `json:"-"`
	Merge   string   `json:"merge_commit_sha"`
}

type prJSON struct {
	PR
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	LabelsJSON []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (p prJSON) toPR() PR {
	out := p.PR
	out.HeadSHA, out.HeadRef, out.BaseRef, out.BaseSHA, out.Author = p.Head.SHA, p.Head.Ref, p.Base.Ref, p.Base.SHA, p.User.Login
	out.Merged = out.Merged || p.MergedAt != nil
	for _, l := range p.LabelsJSON {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

// CreatePR opens a pull request and returns it.
func (c *Client) CreatePR(ctx context.Context, title, body, head, base string) (*PR, error) {
	var out prJSON
	if err := c.do(ctx, http.MethodPost, c.repoPath("/pulls"), map[string]string{"title": title, "body": body, "head": head, "base": base}, &out); err != nil {
		return nil, err
	}
	pr := out.toPR()
	return &pr, nil
}

// AddLabels labels an issue or pull request.
func (c *Client) AddLabels(ctx context.Context, number int, labels ...string) error {
	return c.do(ctx, http.MethodPost, c.repoPath("/issues/%d/labels", number), map[string][]string{"labels": labels}, nil)
}

// GetPR reads one pull request.
func (c *Client) GetPR(ctx context.Context, number int) (*PR, error) {
	var out prJSON
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls/%d", number), nil, &out); err != nil {
		return nil, err
	}
	pr := out.toPR()
	return &pr, nil
}

// PRsForBranch lists pull requests whose head is branch in this repository, newest first.
func (c *Client) PRsForBranch(ctx context.Context, branch, state string) ([]PR, error) {
	owner := strings.SplitN(c.repo, "/", 2)[0]
	var out []prJSON
	q := url.Values{"head": {owner + ":" + branch}, "state": {state}, "per_page": {"30"}, "sort": {"created"}, "direction": {"desc"}}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls?%s", q.Encode()), nil, &out); err != nil {
		return nil, err
	}
	prs := make([]PR, len(out))
	for i, p := range out {
		prs[i] = p.toPR()
	}
	return prs, nil
}

// LastCommitFor returns the newest commit on ref that changed path.
func (c *Client) LastCommitFor(ctx context.Context, path, ref string) (string, error) {
	var out []struct {
		SHA string `json:"sha"`
	}
	q := url.Values{"path": {path}, "sha": {ref}, "per_page": {"1"}}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/commits?%s", q.Encode()), nil, &out); err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", ErrNotFound
	}
	return out[0].SHA, nil
}

// PRsForCommit lists the pull requests a commit belongs to (for a merge commit:
// the pull request it merged).
func (c *Client) PRsForCommit(ctx context.Context, sha string) ([]PR, error) {
	var out []prJSON
	if err := c.do(ctx, http.MethodGet, c.repoPath("/commits/%s/pulls", sha), nil, &out); err != nil {
		return nil, err
	}
	prs := make([]PR, len(out))
	for i, p := range out {
		prs[i] = p.toPR()
	}
	return prs, nil
}

// Reviews lists a pull request's reviews in chronological order.
func (c *Client) Reviews(ctx context.Context, number int) ([]access.Review, error) {
	var out []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State    string `json:"state"`
		CommitID string `json:"commit_id"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls/%d/reviews?per_page=100", number), nil, &out); err != nil {
		return nil, err
	}
	rs := make([]access.Review, len(out))
	for i, r := range out {
		rs[i] = access.Review{User: r.User.Login, State: r.State, CommitID: r.CommitID}
	}
	return rs, nil
}

// AppTokenSource authenticates as a GitHub App installation: it signs a short
// JWT with the App's private key and exchanges it for an installation token,
// cached until shortly before it expires.
func AppTokenSource(baseURL string, appID, installationID int64, privateKeyPEM []byte) (TokenSource, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, errors.New("GitHub App private key: not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k8, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App private key: not RSA")
		}
		key = rk
	} else {
		return nil, errors.New("GitHub App private key: unreadable")
	}
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	var (
		mu      sync.Mutex
		token   string
		expires time.Time
	)
	hc := &http.Client{Timeout: 20 * time.Second}
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if token != "" && time.Until(expires) > 5*time.Minute {
			return token, nil
		}
		now := time.Now()
		jwt, err := signJWT(key, map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": fmt.Sprint(appID)})
		if err != nil {
			return "", err
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(baseURL, "/"), installationID), nil)
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var out struct {
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expires_at"`
			Message   string    `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
		if resp.StatusCode != http.StatusCreated || out.Token == "" {
			return "", fmt.Errorf("installation token: HTTP %d %s", resp.StatusCode, out.Message)
		}
		token, expires = out.Token, out.ExpiresAt
		return token, nil
	}, nil
}

func signJWT(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	enc := base64.RawURLEncoding
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	c, _ := json.Marshal(claims)
	unsigned := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + enc.EncodeToString(sig), nil
}
