package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// StartSession opens a session for an already-authenticated user (password or SSO).
func (s *Service) StartSession(ctx context.Context, userID string, ip netip.Addr, userAgent string) (string, *User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `SELECT id, email, name, role FROM users WHERE id = $1 AND NOT disabled`, userID).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
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
	now := s.now()
	if _, err := s.pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at, ip, user_agent) VALUES ($1,$2,$3,$4,$5)`,
		u.tokenHash, u.ID, now.Add(s.maxAge), ipArg, truncate(userAgent, 512)); err != nil {
		return "", nil, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE users SET last_login_at = $2, failed_logins = 0, locked_until = NULL WHERE id = $1`, u.ID, now)
	return token, &u, nil
}

var ErrWrongPassword = errors.New("current password is incorrect")

// ChangePassword verifies the current password, stores the new hash, and ends
// every other session of the user.
func (s *Service) ChangePassword(ctx context.Context, u *User, current, next string) error {
	var hash *string
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, u.ID).Scan(&hash); err != nil {
		return err
	}
	if hash != nil {
		ok, err := VerifyPassword(current, *hash)
		if err != nil {
			return err
		}
		if !ok {
			return ErrWrongPassword
		}
	}
	nh, err := HashPassword(next)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, u.ID, nh); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, u.ID, u.tokenHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Account is a user as administrators see it.
type Account struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        Role       `json:"role"`
	Disabled    bool       `json:"disabled"`
	HasPassword bool       `json:"has_password"`
	LockedUntil *time.Time `json:"locked_until"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, email, name, role, disabled, password_hash IS NOT NULL, locked_until, last_login_at, created_at
		FROM users ORDER BY disabled, lower(email)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Disabled, &a.HasPassword, &a.LockedUntil, &a.LastLoginAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) GetAccount(ctx context.Context, id string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx, `SELECT id, email, name, role, disabled, password_hash IS NOT NULL, locked_until, last_login_at, created_at
		FROM users WHERE id = $1`, id).Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Disabled, &a.HasPassword, &a.LockedUntil, &a.LastLoginAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	return &a, err
}

var (
	ErrEmailTaken = errors.New("a user with this email already exists")
	ErrLastAdmin  = errors.New("at least one enabled admin must remain")
)

// CreateAccount adds a user. An empty password creates an SSO-only account.
func (s *Service) CreateAccount(ctx context.Context, email, name string, role Role, password string) (*Account, error) {
	var hash any
	if password != "" {
		h, err := HashPassword(password)
		if err != nil {
			return nil, err
		}
		hash = h
	}
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO users (email, name, password_hash, role) VALUES ($1,$2,$3,$4) RETURNING id`,
		strings.ToLower(email), name, hash, string(role)).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "users_email_lower") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return s.GetAccount(ctx, id)
}

type AccountPatch struct {
	Name     *string
	Role     *Role
	Disabled *bool
	Password *string // admin reset
	Unlock   bool
}

// UpdateAccount applies p, refusing changes that would leave no enabled admin.
func (s *Service) UpdateAccount(ctx context.Context, id string, p AccountPatch) (*Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var hash any
	if p.Password != nil {
		h, err := HashPassword(*p.Password)
		if err != nil {
			return nil, err
		}
		hash = h
	}
	var role *string
	if p.Role != nil {
		r := string(*p.Role)
		role = &r
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET name = coalesce($2, name), role = coalesce($3, role), disabled = coalesce($4, disabled),
		password_hash = coalesce($5, password_hash),
		locked_until = CASE WHEN $6 THEN NULL ELSE locked_until END, failed_logins = CASE WHEN $6 THEN 0 ELSE failed_logins END
		WHERE id = $1`, id, p.Name, role, p.Disabled, hash, p.Unlock)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNoSession
	}
	var admins int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled`).Scan(&admins); err != nil {
		return nil, err
	}
	if admins == 0 {
		return nil, ErrLastAdmin
	}
	if (p.Disabled != nil && *p.Disabled) || p.Password != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, id)
}

// FindOrProvisionSSO returns the user for a verified Google email. Unknown users are
// created as viewers only when their Google Workspace domain matches allowedDomain.
func (s *Service) FindOrProvisionSSO(ctx context.Context, email, name, hostedDomain, allowedDomain string) (string, error) {
	email = strings.ToLower(email)
	var id string
	var disabled bool
	err := s.pool.QueryRow(ctx, `SELECT id, disabled FROM users WHERE lower(email) = $1`, email).Scan(&id, &disabled)
	if err == nil {
		if disabled {
			return "", ErrInvalidCredentials
		}
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if allowedDomain == "" || !strings.EqualFold(hostedDomain, allowedDomain) {
		return "", ErrInvalidCredentials
	}
	if name == "" {
		name = email
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO users (email, name, role) VALUES ($1, $2, 'viewer') RETURNING id`, email, name).Scan(&id)
	return id, err
}
