// Package db opens the PostgreSQL pool and applies the embedded migrations.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bm-monitor/api/migrations"
)

// Open connects to PostgreSQL, retrying for up to wait so the API can start
// slightly before the database is ready.
func Open(ctx context.Context, url string, wait time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		time.Sleep(time.Second)
	}
}

func newProvider(pool *pgxpool.Pool) (*goose.Provider, func() error, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	sub, err := fs.Sub(migrations.FS, ".")
	if err != nil {
		return nil, nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		sqlDB.Close()
		return nil, nil, err
	}
	return p, sqlDB.Close, nil
}

// Migrate applies every pending migration.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	results, err := p.Up(ctx)
	for _, r := range results {
		log.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// MigrateTo moves the schema up or down to version. Used by migration tests.
func MigrateTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	cur, err := p.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	if version < cur {
		_, err = p.DownTo(ctx, version)
	} else {
		_, err = p.UpTo(ctx, version)
	}
	return err
}

// MigrateDownAll rolls back every migration. Used by the migration round-trip test.
func MigrateDownAll(ctx context.Context, pool *pgxpool.Pool) error {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	_, err = p.DownTo(ctx, 0)
	return err
}

// SeedSetting stores value under key only if the key does not exist yet,
// so values changed later through the UI are never overwritten by env.
func SeedSetting(ctx context.Context, pool *pgxpool.Pool, key string, value any) error {
	raw, err := JSONB(value)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, key, raw)
	return err
}

// JSONB marshals v for a jsonb parameter. Always use it instead of passing v
// directly: pgx sends a Go string as raw text, which is not valid JSON
// (a string must arrive as "\"USD\"", not "USD"). nil becomes SQL NULL.
func JSONB(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal jsonb: %w", err)
	}
	return string(b), nil
}

// IsUniqueViolation reports whether err is a PostgreSQL unique or exclusion
// violation, returning the constraint name.
func IsUniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23P01") {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// IsForeignKeyViolation reports whether err is a PostgreSQL foreign key violation.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// IsNoRows reports whether err means the row was not found.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
