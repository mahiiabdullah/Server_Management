package config

import (
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":         "postgres://bm:pw@postgres:5432/bm?sslmode=disable",
		"SESSION_SECRET":       strings.Repeat("s", 32),
		"CSRF_SECRET":          strings.Repeat("c", 32),
		"PROMETHEUS_URL":       "http://prometheus:9090",
		"TARGETS_FILE":         "/targets/fleet.json",
		"PUBLIC_URL":           "https://monitor.example.com",
		"ALLOWED_TARGET_CIDRS": "10.0.0.0/8, 103.174.50.0/24",
	}
}

func load(env map[string]string) (*Config, error) {
	return LoadFrom(func(k string) string { return env[k] })
}

func TestLoadValid(t *testing.T) {
	c, err := load(validEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.AllowedTargetCIDRs) != 2 || c.AllowedTargetCIDRs[1].String() != "103.174.50.0/24" {
		t.Errorf("cidrs = %v", c.AllowedTargetCIDRs)
	}
	if len(c.AllowedTargetPorts) != 1 || c.AllowedTargetPorts[0] != 9100 {
		t.Errorf("ports = %v, want default [9100]", c.AllowedTargetPorts)
	}
	if c.DefaultScrapeInterval != 30*time.Second || c.BillingTimezone.String() != "UTC" || c.BillingCurrency != "USD" {
		t.Errorf("defaults not applied: %+v", c)
	}
	if !c.SecureCookies() {
		t.Error("https PUBLIC_URL must give secure cookies")
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	env := validEnv()
	delete(env, "DATABASE_URL")
	env["SESSION_SECRET"] = "short"
	env["ALLOWED_TARGET_CIDRS"] = "10.0.0.0/8,not-a-cidr"
	env["ALLOWED_TARGET_PORTS"] = "9100,0"
	env["BILLING_TIMEZONE"] = "Mars/Olympus"
	env["ADMIN_EMAIL"] = "admin@example.com"
	env["PUBLIC_URL"] = "monitor.example.com"

	_, err := load(env)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"DATABASE_URL is required",
		"SESSION_SECRET must be at least 32",
		`invalid CIDR "not-a-cidr"`,
		`invalid port "0"`,
		`unknown timezone "Mars/Olympus"`,
		"ADMIN_EMAIL and ADMIN_PASSWORD must be set together",
		"PUBLIC_URL must be an absolute",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

func TestMissingPrometheusURLReportedOnce(t *testing.T) {
	env := validEnv()
	delete(env, "PROMETHEUS_URL")
	_, err := load(env)
	if n := strings.Count(err.Error(), "PROMETHEUS_URL"); n != 1 {
		t.Errorf("PROMETHEUS_URL mentioned %d times, want 1:\n%s", n, err)
	}
}

func TestShortAdminPasswordRejected(t *testing.T) {
	env := validEnv()
	env["ADMIN_EMAIL"] = "Admin@Example.com"
	env["ADMIN_PASSWORD"] = "short"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "at least 12") {
		t.Fatalf("want short-password error, got %v", err)
	}
	env["ADMIN_PASSWORD"] = "a-long-enough-password"
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminEmail != "admin@example.com" {
		t.Errorf("admin email not lower-cased: %q", c.AdminEmail)
	}
}
