package db_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"bm-monitor/api/internal/db"
	"bm-monitor/api/internal/dbtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	dbtest.StopEmbedded()
	os.Exit(code)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// Up on an empty database, down to zero, then up again.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewTestDB(t)

	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := db.MigrateDownAll(ctx, pool); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name <> 'goose_db_version'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d tables left after down", n)
	}
	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("second up: %v", err)
	}
}

// Regression: a plain Go string must be stored as a JSON string, and a seeded
// key must never overwrite a value that already exists.
func TestSeedSetting(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewTestDB(t)
	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedSetting(ctx, pool, "billing_currency", "USD"); err != nil {
		t.Fatalf("seed string: %v", err)
	}
	if err := db.SeedSetting(ctx, pool, "billing_currency", "EUR"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT value #>> '{}' FROM settings WHERE key = 'billing_currency'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "USD" {
		t.Errorf("billing_currency = %q, want USD (existing value must win)", got)
	}
	if err := db.SeedSetting(ctx, pool, "obj", map[string]any{"enabled": true}); err != nil {
		t.Fatalf("seed object: %v", err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewTestDB(t)
	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}

	insertServer := func(name, url, addr string) (string, error) {
		var id string
		err := pool.QueryRow(ctx,
			`INSERT INTO servers (name, hostname, exporter_url, target_addr) VALUES ($1, $1, $2, $3) RETURNING id`,
			name, url, addr).Scan(&id)
		return id, err
	}
	id, err := insertServer("bm1", "http://10.0.0.11:9100/metrics", "10.0.0.11:9100")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("duplicate live name is rejected case-insensitively", func(t *testing.T) {
		_, err := insertServer("BM1", "http://10.0.0.12:9100/metrics", "10.0.0.12:9100")
		if c, ok := db.IsUniqueViolation(err); !ok || c != "servers_name_live" {
			t.Fatalf("want servers_name_live violation, got %v", err)
		}
	})
	t.Run("duplicate target is rejected", func(t *testing.T) {
		_, err := insertServer("bm9", "http://bm9:9100/metrics", "10.0.0.11:9100")
		if c, ok := db.IsUniqueViolation(err); !ok || c != "servers_target_live" {
			t.Fatalf("want servers_target_live violation, got %v", err)
		}
	})
	t.Run("soft-deleted server frees its name", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE servers SET deleted_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := insertServer("bm1", "http://10.0.0.11:9100/metrics", "10.0.0.11:9100"); err != nil {
			t.Fatalf("re-register after delete: %v", err)
		}
	})
	t.Run("overlapping pricing for one server is rejected", func(t *testing.T) {
		var planA, planB string
		pool.QueryRow(ctx, `INSERT INTO pricing_plans (name,currency,amount,unit,category) VALUES ('a','USD',100,'month','compute') RETURNING id`).Scan(&planA)
		pool.QueryRow(ctx, `INSERT INTO pricing_plans (name,currency,amount,unit,category) VALUES ('b','USD',1,'hour','compute') RETURNING id`).Scan(&planB)
		from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		if _, err := pool.Exec(ctx, `INSERT INTO server_pricing (server_id, plan_id, effective_from, effective_to) VALUES ($1,$2,$3,$4)`,
			id, planA, from, from.AddDate(0, 0, 10)); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, `INSERT INTO server_pricing (server_id, plan_id, effective_from) VALUES ($1,$2,$3)`,
			id, planB, from.AddDate(0, 0, 5))
		if _, ok := db.IsUniqueViolation(err); !ok {
			t.Fatalf("want exclusion violation, got %v", err)
		}
		// Adjacent ranges ([from,to) then [to,...)) are allowed.
		if _, err := pool.Exec(ctx, `INSERT INTO server_pricing (server_id, plan_id, effective_from) VALUES ($1,$2,$3)`,
			id, planB, from.AddDate(0, 0, 10)); err != nil {
			t.Fatalf("adjacent range rejected: %v", err)
		}
	})
	t.Run("one open alert per fingerprint", func(t *testing.T) {
		var rule string
		pool.QueryRow(ctx, `INSERT INTO alert_rules (name,kind,threshold,severity) VALUES ('cpu','cpu',90,'warning') RETURNING id`).Scan(&rule)
		ins := `INSERT INTO alerts (rule_id, fingerprint, state, severity, message, started_at) VALUES ($1,'fp1',$2,'warning','x',now())`
		if _, err := pool.Exec(ctx, ins, rule, "firing"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, ins, rule, "pending"); err == nil {
			t.Fatal("second open alert with the same fingerprint was accepted")
		}
		if _, err := pool.Exec(ctx, `UPDATE alerts SET state='resolved', resolved_at=now() WHERE fingerprint='fp1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, ins, rule, "pending"); err != nil {
			t.Fatalf("new incident after resolve rejected: %v", err)
		}
	})
}

// 0004 moves each server's single group into server_group_members, and back on down.
func TestGroupMembersMigrationKeepsGroups(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewTestDB(t)
	if err := db.MigrateTo(ctx, pool, 3); err != nil {
		t.Fatal(err)
	}
	var g, s1, s2 string
	if err := pool.QueryRow(ctx, `INSERT INTO server_groups (name) VALUES ('puku-bot') RETURNING id`).Scan(&g); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO servers (name, hostname, exporter_url, target_addr, group_id) VALUES ($1, $1, $2, $2, $3) RETURNING id`
	if err := pool.QueryRow(ctx, ins, "bm1", "10.0.0.1:9100", g).Scan(&s1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, ins, "bm2", "10.0.0.2:9100", nil).Scan(&s2); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	var members []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(server_id::text) FROM server_group_members WHERE group_id = $1`, g).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0] != s1 {
		t.Fatalf("members after up = %v, want [%s]", members, s1)
	}
	if err := db.MigrateTo(ctx, pool, 3); err != nil {
		t.Fatal(err)
	}
	var back *string
	if err := pool.QueryRow(ctx, `SELECT group_id::text FROM servers WHERE id = $1`, s1).Scan(&back); err != nil {
		t.Fatal(err)
	}
	if back == nil || *back != g {
		t.Fatalf("group after down = %v, want %s", back, g)
	}
}
