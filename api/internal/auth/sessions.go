package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrNoSession          = errors.New("no valid session")
)

// LockedError means the account is temporarily locked after too many failed logins.
type LockedError struct{ Until time.Time }

func (e *LockedError) Error() string { return "account temporarily locked" }

// Lockout policy: after MaxFailedLogins consecutive failures the account is locked for LockoutPeriod.
const (
	MaxFailedLogins = 5
	LockoutPeriod   = 15 * time.Minute
	touchEvery      = time.Minute // how often last_seen_at is refreshed
)

type Service struct {
	pool       *pgxpool.Pool
	csrfSecret []byte
	idle       time.Duration
	maxAge     time.Duration
	now        func() time.Time
}

func NewService(pool *pgxpool.Pool, csrfSecret []byte, idle, maxAge time.Duration) *Service {
	return &Service{pool: pool, csrfSecret: csrfSecret, idle: idle, maxAge: maxAge, now: time.Now}
}

// MaxAge is the absolute session lifetime, used for the cookie expiry.
func (s *Service) MaxAge() time.Duration { return s.maxAge }

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CSRFToken derives the CSRF token bound to a session. It is not stored anywhere.
func (s *Service) CSRFToken(u *User) string {
	m := hmac.New(sha256.New, s.csrfSecret)
	m.Write(u.tokenHash)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCSRF compares the submitted token with the one bound to the session.
func (s *Service) CheckCSRF(u *User, submitted string) bool {
	if u == nil || submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.CSRFToken(u)), []byte(submitted)) == 1
}

// Login verifies credentials, applies the lockout policy and opens a session.
// It returns the raw session token for the cookie.
func (s *Service) Login(ctx context.Context, email, password string, ip netip.Addr, userAgent string) (string, *User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var (
		u           User
		hash        *string
		disabled    bool
		lockedUntil *time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, role, password_hash, disabled, locked_until FROM users WHERE lower(email) = $1`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &hash, &disabled, &lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = VerifyPassword(password, dummyHash) // equalise timing
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	if lockedUntil != nil && lockedUntil.After(now) {
		return "", nil, &LockedError{Until: *lockedUntil}
	}
	ok := false
	if hash != nil {
		if ok, err = VerifyPassword(password, *hash); err != nil {
			return "", nil, fmt.Errorf("user %s: %w", u.ID, err)
		}
	} else {
		_, _ = VerifyPassword(password, dummyHash) // SSO-only account: no password login
	}
	if !ok || disabled {
		var lockedNow *time.Time
		err := s.pool.QueryRow(ctx, `
			UPDATE users SET
			  failed_logins = CASE WHEN failed_logins + 1 >= $2 THEN 0 ELSE failed_logins + 1 END,
			  locked_until  = CASE WHEN failed_logins + 1 >= $2 THEN $3 ELSE locked_until END
			WHERE id = $1 RETURNING locked_until`,
			u.ID, MaxFailedLogins, now.Add(LockoutPeriod)).Scan(&lockedNow)
		if err != nil {
			return "", nil, err
		}
		if lockedNow != nil && lockedNow.After(now) {
			return "", nil, &LockedError{Until: *lockedNow}
		}
		return "", nil, ErrInvalidCredentials
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	u.tokenHash = hashToken(token)

	var ipArg any
	if ip.IsValid() {
		ipArg = ip.String()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, ip, user_agent) VALUES ($1, $2, $3, $4, $5)`,
		u.tokenHash, u.ID, now.Add(s.maxAge), ipArg, truncate(userAgent, 512)); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = $2 WHERE id = $1`, u.ID, now); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	return token, &u, nil
}

// Authenticate resolves a session token to its user, enforcing absolute and idle expiry.
func (s *Service) Authenticate(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	th := hashToken(token)
	now := s.now()
	var (
		u        User
		lastSeen time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.role, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2 AND s.last_seen_at > $3 AND NOT u.disabled`,
		th, now, now.Add(-s.idle)).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &lastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	if now.Sub(lastSeen) > touchEvery {
		if _, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE token_hash = $1`, th, now); err != nil {
			return nil, err
		}
	}
	u.tokenHash = th
	return &u, nil
}

// Logout deletes the session behind u.
func (s *Service) Logout(ctx context.Context, u *User) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, u.tokenHash)
	return err
}

// DeleteExpired removes sessions past their absolute or idle expiry.
func (s *Service) DeleteExpired(ctx context.Context) (int64, error) {
	now := s.now()
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at <= $2`, now, now.Add(-s.idle))
	return tag.RowsAffected(), err
}

// RunCleanup deletes expired sessions every interval until ctx ends.
func (s *Service) RunCleanup(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.DeleteExpired(ctx); err != nil && ctx.Err() == nil {
				log.Error("session cleanup failed", "err", err)
			} else if n > 0 {
				log.Debug("expired sessions removed", "count", n)
			}
		}
	}
}

// EnsureAdmin creates the first admin when the users table is empty.
// It does nothing once any user exists, so changing ADMIN_PASSWORD later has no effect.
func EnsureAdmin(ctx context.Context, pool *pgxpool.Pool, email, password string, log *slog.Logger) error {
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if email == "" {
		log.Warn("no users exist and ADMIN_EMAIL is not set; nobody can sign in")
		return nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `
		INSERT INTO users (email, name, password_hash, role)
		SELECT $1, 'Administrator', $2, 'admin' WHERE NOT EXISTS (SELECT 1 FROM users)`, email, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		log.Info("first admin created; change its password after signing in", "email", email)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
