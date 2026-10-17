// Package inventory manages servers, groups and labels, and validates Node
// Exporter endpoints before anything is registered (the SSRF guard).
package inventory

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Guard decides whether an exporter URL may be scraped, and probes it.
type Guard struct {
	Allowed      []netip.Prefix
	Denied       []netip.Prefix
	AllowedPorts []uint16
	BasicUser    string
	BasicPass    string
	// Resolver is replaceable in tests.
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
}

// Endpoint is a parsed, validated exporter URL.
type Endpoint struct {
	Scheme     string
	Host       string // as entered (name or IP)
	Port       uint16
	Path       string
	Addr       netip.Addr // the single resolved address that will be scraped
	TargetAddr string     // "ip:port" written to Prometheus
	CleanURL   string     // normalised URL stored in the database
}

// FieldError is a validation problem tied to one input field.
type FieldError struct {
	Field, Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Check parses rawURL, resolves the host once and verifies every resolved
// address is allowed. It never contacts the target.
func (g *Guard) Check(ctx context.Context, rawURL string) (*Endpoint, error) {
	bad := func(msg string) error { return &FieldError{"exporter_url", msg} }
	rawURL = strings.TrimSpace(rawURL)
	if len(rawURL) > 512 {
		return nil, bad("is too long")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, bad("is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, bad("must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, bad("must not contain credentials, a query or a fragment")
	}
	host := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		portStr = "9100"
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || !slices.Contains(g.AllowedPorts, uint16(port)) {
		return nil, bad(fmt.Sprintf("port %s is not allowed (allowed: %s)", portStr, joinPorts(g.AllowedPorts)))
	}
	path := u.EscapedPath()
	if path == "" || path == "/" {
		path = "/metrics"
	}
	if !validPath.MatchString(path) {
		return nil, bad("path contains unsupported characters")
	}

	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip.Unmap()}
	} else {
		if !validHostname.MatchString(host) {
			return nil, bad("host is not a valid hostname or IP address")
		}
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		res := g.Resolver
		if res == nil {
			res = net.DefaultResolver
		}
		addrs, err = res.LookupNetIP(rctx, "ip", host)
		if err != nil || len(addrs) == 0 {
			return nil, bad("host name could not be resolved")
		}
	}
	// Every resolved address must pass, so a name that points at both an allowed and
	// a forbidden address is refused.
	for _, a := range addrs {
		a = a.Unmap()
		if msg := g.addrProblem(a); msg != "" {
			return nil, bad(msg)
		}
	}
	addr := addrs[0].Unmap()
	for _, a := range addrs { // prefer IPv4 for stable targets
		if a.Unmap().Is4() {
			addr = a.Unmap()
			break
		}
	}
	target := netip.AddrPortFrom(addr, uint16(port)).String()
	clean := (&url.URL{Scheme: u.Scheme, Host: net.JoinHostPort(host, portStr), Path: path}).String()
	return &Endpoint{Scheme: u.Scheme, Host: host, Port: uint16(port), Path: path, Addr: addr, TargetAddr: target, CleanURL: clean}, nil
}

func (g *Guard) addrProblem(a netip.Addr) string {
	for _, p := range g.Denied {
		if p.Contains(a) {
			return fmt.Sprintf("address %s is in a forbidden range (%s)", a, p)
		}
	}
	for _, p := range g.Allowed {
		if p.Contains(a) {
			return ""
		}
	}
	return fmt.Sprintf("address %s is not in an allowed network (ALLOWED_TARGET_CIDRS)", a)
}

var (
	validHostname = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	validPath     = regexp.MustCompile(`^/[A-Za-z0-9/_.\-]*$`)
	// [{,] anchors the label name, so goversion="go1.x" is never mistaken for version.
	versionRe = regexp.MustCompile(`node_exporter_build_info\{(?:[^}]*,)?version="([^"]*)"`)
)

func joinPorts(ps []uint16) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(int(p))
	}
	return strings.Join(s, ", ")
}

// ProbeResult is what a test fetch of /metrics found.
type ProbeResult struct {
	Reachable       bool   `json:"reachable"`
	NodeExporter    bool   `json:"node_exporter"`
	ExporterVersion string `json:"node_exporter_version,omitempty"`
	LatencyMS       int64  `json:"latency_ms"`
	Error           string `json:"error,omitempty"`
}

const maxProbeBody = 2 << 20

// Probe fetches the endpoint once: 3 s timeout, no redirects, 2 MiB limit, and
// the TCP connection is pinned to the already-validated address (no second DNS
// lookup, so DNS rebinding cannot redirect it).
func (g *Guard) Probe(ctx context.Context, ep *Endpoint) ProbeResult {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	target := ep.TargetAddr
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, target)
		},
		TLSClientConfig:       &tls.Config{ServerName: ep.Host, MinVersion: tls.VersionTLS12},
		ResponseHeaderTimeout: 3 * time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport:     tr,
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.CleanURL, nil)
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	if g.BasicUser != "" {
		req.SetBasicAuth(g.BasicUser, g.BasicPass)
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return ProbeResult{LatencyMS: latency, Error: probeError(err)}
	}
	defer resp.Body.Close()
	res := ProbeResult{Reachable: true, LatencyMS: latency}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("endpoint answered HTTP %d", resp.StatusCode)
		return res
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxProbeBody))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "node_exporter_build_info{") {
			res.NodeExporter = true
			if m := versionRe.FindStringSubmatch(line); m != nil {
				res.ExporterVersion = m[1]
			}
			break
		}
	}
	if !res.NodeExporter {
		res.Error = "endpoint does not look like Node Exporter (no node_exporter_build_info metric)"
	}
	return res
}

func probeError(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "connection timed out after 3 s"
	case strings.Contains(err.Error(), "refused"):
		return "connection refused (is Node Exporter running and is the firewall open to the monitoring host?)"
	case strings.Contains(err.Error(), "certificate"):
		return "TLS certificate is not trusted"
	default:
		return "could not connect"
	}
}
