// Package config loads and validates the API configuration from environment variables.
// Start-up fails with one message listing every problem, so an operator can fix them all at once.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL   string
	SessionSecret []byte
	CSRFSecret    []byte
	AdminEmail    string
	AdminPassword string
	PrometheusURL string
	TargetsFile   string
	PublicURL     *url.URL
	ListenAddr    string

	AllowedTargetCIDRs []netip.Prefix
	DeniedTargetCIDRs  []netip.Prefix // always includes loopback, link-local, metadata, unspecified
	AllowedTargetPorts []uint16

	NodeExporterUser     string
	NodeExporterPassword string

	SMTP              SMTPConfig
	SlackWebhookURL   string
	TelegramBotToken  string
	TelegramChatID    string
	DiscordWebhookURL string

	GoogleClientID      string
	GoogleClientSecret  string
	GoogleAllowedDomain string

	// SSH access workflow (optional): the repository holding access/grants.yaml,
	// and either a fine-grained token or a GitHub App to open pull requests with.
	GitHubRepo           string // owner/name
	GitHubAPIURL         string
	GitHubToken          string
	GitHubAppID          int64
	GitHubInstallationID int64
	GitHubAppKeyFile     string

	DefaultScrapeInterval time.Duration
	AlertEvalInterval     time.Duration
	BillingTimezone       *time.Location
	BillingCurrency       string
	LogLevel              slog.Level

	SessionIdleTimeout time.Duration
	SessionMaxAge      time.Duration
}

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads the configuration through getenv, which makes it testable.
func LoadFrom(getenv func(string) string) (*Config, error) {
	var errs []error
	req := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	opt := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	secret := func(key string) []byte {
		v := req(key)
		if v != "" && len(v) < 32 {
			errs = append(errs, fmt.Errorf("%s must be at least 32 characters (use: openssl rand -base64 32)", key))
		}
		return []byte(v)
	}
	dur := func(key, def string, min time.Duration) time.Duration {
		raw := opt(key, def)
		d, err := time.ParseDuration(raw)
		if err != nil || d < min {
			errs = append(errs, fmt.Errorf("%s must be a duration of at least %s, got %q", key, min, raw))
		}
		return d
	}

	c := &Config{
		DatabaseURL:   req("DATABASE_URL"),
		SessionSecret: secret("SESSION_SECRET"),
		CSRFSecret:    secret("CSRF_SECRET"),
		AdminEmail:    strings.ToLower(opt("ADMIN_EMAIL", "")),
		AdminPassword: opt("ADMIN_PASSWORD", ""),
		PrometheusURL: req("PROMETHEUS_URL"),
		TargetsFile:   req("TARGETS_FILE"),
		ListenAddr:    opt("LISTEN_ADDR", ":8080"),

		DefaultScrapeInterval: dur("DEFAULT_SCRAPE_INTERVAL", "30s", 10*time.Second),
		AlertEvalInterval:     dur("ALERT_EVAL_INTERVAL", "30s", 10*time.Second),
		SessionIdleTimeout:    dur("SESSION_IDLE_TIMEOUT", "12h", 5*time.Minute),
		SessionMaxAge:         dur("SESSION_MAX_AGE", "168h", time.Hour),
		BillingCurrency:       strings.ToUpper(opt("BILLING_CURRENCY", "USD")),
	}

	if (c.AdminEmail == "") != (c.AdminPassword == "") {
		errs = append(errs, errors.New("ADMIN_EMAIL and ADMIN_PASSWORD must be set together"))
	}
	if c.AdminPassword != "" && len(c.AdminPassword) < 12 {
		errs = append(errs, errors.New("ADMIN_PASSWORD must be at least 12 characters"))
	}

	if raw := req("PUBLIC_URL"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("PUBLIC_URL must be an absolute http(s) URL, got %q", raw))
		} else {
			c.PublicURL = u
		}
	}
	if raw := c.PrometheusURL; raw != "" {
		if u, err := url.Parse(raw); err != nil || u.Host == "" {
			errs = append(errs, fmt.Errorf("PROMETHEUS_URL must be an absolute URL, got %q", raw))
		}
	}

	for _, part := range splitList(req("ALLOWED_TARGET_CIDRS")) {
		p, err := netip.ParsePrefix(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("ALLOWED_TARGET_CIDRS: invalid CIDR %q", part))
			continue
		}
		c.AllowedTargetCIDRs = append(c.AllowedTargetCIDRs, p.Masked())
	}
	c.DeniedTargetCIDRs = AlwaysDenied()
	for _, part := range splitList(opt("DENIED_TARGET_CIDRS", "")) {
		p, err := netip.ParsePrefix(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("DENIED_TARGET_CIDRS: invalid CIDR %q", part))
			continue
		}
		c.DeniedTargetCIDRs = append(c.DeniedTargetCIDRs, p.Masked())
	}

	c.NodeExporterUser = opt("NODE_EXPORTER_BASIC_AUTH_USER", "")
	c.NodeExporterPassword = opt("NODE_EXPORTER_BASIC_AUTH_PASSWORD", "")
	if (c.NodeExporterUser == "") != (c.NodeExporterPassword == "") {
		errs = append(errs, errors.New("NODE_EXPORTER_BASIC_AUTH_USER and NODE_EXPORTER_BASIC_AUTH_PASSWORD must be set together"))
	}

	c.SMTP = SMTPConfig{
		Host:     opt("SMTP_HOST", ""),
		User:     opt("SMTP_USER", ""),
		Password: strings.ReplaceAll(opt("SMTP_PASSWORD", ""), " ", ""), // Google shows app passwords with spaces
		From:     opt("SMTP_FROM", ""),
	}
	if c.SMTP.Host != "" {
		port, err := strconv.Atoi(opt("SMTP_PORT", "587"))
		if err != nil || port <= 0 || port > 65535 {
			errs = append(errs, errors.New("SMTP_PORT must be a port number"))
		}
		c.SMTP.Port = port
		if c.SMTP.From == "" {
			c.SMTP.From = c.SMTP.User
		}
		if _, err := mail.ParseAddress(c.SMTP.From); err != nil {
			errs = append(errs, fmt.Errorf("SMTP_FROM must be an email address, got %q", c.SMTP.From))
		}
	}
	c.SlackWebhookURL = opt("SLACK_WEBHOOK_URL", "")
	c.TelegramBotToken = opt("TELEGRAM_BOT_TOKEN", "")
	c.TelegramChatID = opt("TELEGRAM_CHAT_ID", "")
	c.DiscordWebhookURL = opt("DISCORD_WEBHOOK_URL", "")

	c.GoogleClientID = opt("GOOGLE_CLIENT_ID", "")
	c.GoogleClientSecret = opt("GOOGLE_CLIENT_SECRET", "")
	c.GoogleAllowedDomain = strings.ToLower(opt("GOOGLE_ALLOWED_DOMAIN", ""))
	if (c.GoogleClientID == "") != (c.GoogleClientSecret == "") {
		errs = append(errs, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set together"))
	}

	c.GitHubRepo = opt("GITHUB_REPO", "")
	c.GitHubAPIURL = opt("GITHUB_API_URL", "")
	c.GitHubToken = opt("GITHUB_TOKEN", "")
	c.GitHubAppKeyFile = opt("GITHUB_APP_PRIVATE_KEY_FILE", "")
	for key, dst := range map[string]*int64{"GITHUB_APP_ID": &c.GitHubAppID, "GITHUB_APP_INSTALLATION_ID": &c.GitHubInstallationID} {
		if v := opt(key, ""); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				errs = append(errs, fmt.Errorf("%s must be a positive number", key))
			}
			*dst = n
		}
	}
	if c.GitHubRepo != "" {
		if strings.Count(c.GitHubRepo, "/") != 1 || strings.HasPrefix(c.GitHubRepo, "http") {
			errs = append(errs, errors.New("GITHUB_REPO must be owner/name, e.g. mahiiabdullah/Server_Management"))
		}
		app := c.GitHubAppID != 0 || c.GitHubInstallationID != 0 || c.GitHubAppKeyFile != ""
		switch {
		case app && (c.GitHubAppID == 0 || c.GitHubInstallationID == 0 || c.GitHubAppKeyFile == ""):
			errs = append(errs, errors.New("a GitHub App needs GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID and GITHUB_APP_PRIVATE_KEY_FILE"))
		case !app && c.GitHubToken == "":
			errs = append(errs, errors.New("GITHUB_REPO is set: also set GITHUB_TOKEN or the GitHub App settings"))
		}
	}

	for _, part := range splitList(opt("ALLOWED_TARGET_PORTS", "9100")) {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil || n == 0 {
			errs = append(errs, fmt.Errorf("ALLOWED_TARGET_PORTS: invalid port %q", part))
			continue
		}
		c.AllowedTargetPorts = append(c.AllowedTargetPorts, uint16(n))
	}

	tz := opt("BILLING_TIMEZONE", "UTC")
	if loc, err := time.LoadLocation(tz); err != nil {
		errs = append(errs, fmt.Errorf("BILLING_TIMEZONE: unknown timezone %q", tz))
	} else {
		c.BillingTimezone = loc
	}
	if len(c.BillingCurrency) != 3 {
		errs = append(errs, fmt.Errorf("BILLING_CURRENCY must be a 3-letter ISO code, got %q", c.BillingCurrency))
	}
	if err := c.LogLevel.UnmarshalText([]byte(opt("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error"))
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %w", joinLines(errs))
	}
	return c, nil
}

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// Enabled reports whether email can be sent.
func (s SMTPConfig) Enabled() bool { return s.Host != "" }

// AccessWorkflow reports whether the SSH access workflow is configured.
func (c *Config) AccessWorkflow() bool { return c.GitHubRepo != "" }

// GoogleSSO reports whether "Sign in with Google" is configured.
func (c *Config) GoogleSSO() bool { return c.GoogleClientID != "" }

// AlwaysDenied are ranges a scrape target may never be in, whatever ALLOWED_TARGET_CIDRS says:
// loopback, link-local (including the 169.254.169.254 cloud metadata address), unspecified,
// multicast and broadcast.
func AlwaysDenied() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "fe80::/10", "ff00::/8", "fd00:ec2::254/128",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// SecureCookies reports whether cookies must carry the Secure flag.
func (c *Config) SecureCookies() bool { return c.PublicURL.Scheme == "https" }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinLines(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return errors.New(strings.Join(msgs, "\n  - "))
}
