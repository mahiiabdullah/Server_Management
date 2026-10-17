// Package dbtest provides real PostgreSQL databases for integration tests.
package dbtest

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Test databases come from TEST_DATABASE_URL when set (CI, Docker), otherwise from
// one embedded PostgreSQL 17 shared by the test binary. Each call to NewTestDB gets
// its own fresh database, so tests never see each other's rows.

var (
	embedOnce sync.Once
	embedURL  string
	embedErr  error
	embedPG   *embeddedpostgres.EmbeddedPostgres
	dbCounter int
	dbMu      sync.Mutex
)

func adminURL() (string, error) {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u, nil
	}
	embedOnce.Do(func() {
		port, err := freePort()
		if err != nil {
			embedErr = err
			return
		}
		base := filepath.Join(os.TempDir(), "bm-monitor-testpg")
		// Test packages run in parallel and share the download cache. When two of them
		// fetch the archive at once, one rename loses on Windows; retrying then finds
		// the archive already cached.
		for attempt := 1; attempt <= 3; attempt++ {
			runtime := filepath.Join(base, fmt.Sprintf("run-%d-%d-%d", os.Getpid(), port, attempt))
			embedPG = embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
				Version(embeddedpostgres.V17).
				Port(uint32(port)).
				RuntimePath(runtime).
				CachePath(filepath.Join(base, "cache")).
				StartTimeout(2 * time.Minute).
				Logger(nil))
			if embedErr = embedPG.Start(); embedErr == nil {
				break
			}
			embedPG = nil
			time.Sleep(time.Duration(attempt) * 3 * time.Second)
		}
		if embedErr != nil {
			return
		}
		embedURL = fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
	})
	return embedURL, embedErr
}

// StopEmbedded stops the shared embedded server; call it from TestMain.
func StopEmbedded() {
	if embedPG != nil {
		_ = embedPG.Stop()
	}
}

// NewTestDB creates an empty database, returns a pool to it, and drops it when the test ends.
func NewTestDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin, err := adminURL()
	if err != nil {
		t.Fatalf("test database unavailable: %v", err)
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	dbMu.Lock()
	dbCounter++
	name := fmt.Sprintf("bmtest_%d_%d", os.Getpid(), dbCounter)
	dbMu.Unlock()
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = adminPool.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adminPool.Close()
	})
	return pool
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
