// Package targets keeps Prometheus' file-based service discovery file in step
// with the PostgreSQL inventory. Prometheus watches the file itself, so no
// reload call (and no lifecycle endpoint) is needed (decision D11).
package targets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Writer struct {
	pool    *pgxpool.Pool
	file    string
	log     *slog.Logger
	mu      sync.Mutex
	trigger chan struct{}
}

func NewWriter(pool *pgxpool.Pool, file string, log *slog.Logger) *Writer {
	return &Writer{pool: pool, file: file, log: log, trigger: make(chan struct{}, 1)}
}

// Group is one file_sd entry.
type Group struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Build returns the target groups for every enabled, live server, sorted by name.
func (w *Writer) Build(ctx context.Context) ([]Group, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT s.id, s.name, s.environment,
		       coalesce((SELECT string_agg(g.name, ',' ORDER BY lower(g.name)) FROM server_group_members m
		                 JOIN server_groups g ON g.id = m.group_id WHERE m.server_id = s.id), ''),
		       s.exporter_url, s.target_addr, s.scrape_interval_s
		FROM servers s
		WHERE s.deleted_at IS NULL AND s.enabled
		ORDER BY lower(s.name), s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var id, name, env, groups, exporterURL, target string
		var interval int
		if err := rows.Scan(&id, &name, &env, &groups, &exporterURL, &target, &interval); err != nil {
			return nil, err
		}
		scheme, path := "http", "/metrics"
		if u, err := url.Parse(exporterURL); err == nil {
			scheme, path = u.Scheme, u.EscapedPath()
		}
		labels := map[string]string{
			"server_id":           id,
			"server_name":         name,
			"environment":         env,
			"__scheme__":          scheme,
			"__metrics_path__":    path,
			"__scrape_interval__": fmt.Sprintf("%ds", interval),
			"__scrape_timeout__":  fmt.Sprintf("%ds", min(10, interval)),
		}
		if groups != "" {
			// Comma-wrapped so one group matches with groups=~".*,name,.*".
			labels["groups"] = "," + groups + ","
		}
		out = append(out, Group{Targets: []string{target}, Labels: labels})
	}
	return out, rows.Err()
}

// Sync rewrites the file if its content differs from the inventory. The write is
// atomic (temp file + rename) so Prometheus never reads a half-written file.
func (w *Writer) Sync(ctx context.Context) (changed bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	groups, err := w.Build(ctx)
	if err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if old, err := os.ReadFile(w.file); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(w.file), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.file), ".fleet-*.json.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), w.file); err != nil {
		return false, err
	}
	w.log.Info("prometheus targets written", "file", w.file, "targets", len(groups))
	return true, nil
}

// Trigger asks the reconciler to sync now. It never blocks.
func (w *Writer) Trigger() {
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

// Run syncs at start-up, after every Trigger, and every interval (reconcile).
func (w *Writer) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := w.Sync(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("targets sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.trigger:
		}
	}
}
