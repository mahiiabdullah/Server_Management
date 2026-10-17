package metrics

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Real filesystems only: pseudo, in-memory and container overlay filesystems are excluded.
const fsFilter = `fstype!~"tmpfs|devtmpfs|overlay|squashfs|ramfs|nsfs|efivarfs|autofs|tracefs|fuse.lxcfs|cgroup2?"`

// Network devices that are not physical uplinks.
const netFilter = `device!~"lo|veth.*|docker.*|br-.*|virbr.*|cali.*|flannel.*|cni.*|tun.*|wg.*"`

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func sel(serverID string) (string, error) {
	if !uuidRe.MatchString(serverID) {
		return "", fmt.Errorf("invalid server id %q", serverID)
	}
	return fmt.Sprintf(`job="node",server_id="%s"`, serverID), nil
}

// Filesystem is one mounted filesystem of a server.
type Filesystem struct {
	Mountpoint string  `json:"mountpoint"`
	Device     string  `json:"device"`
	FSType     string  `json:"fstype"`
	SizeBytes  float64 `json:"size_bytes"`
	AvailBytes float64 `json:"avail_bytes"`
	UsedRatio  float64 `json:"used_ratio"`
}

// Current is the latest known state of one server. Nil means no data, never zero.
type Current struct {
	Up              *bool        `json:"-"`
	LastScrape      *time.Time   `json:"last_scrape"`
	CPU             *float64     `json:"cpu"`    // 0..1
	Memory          *float64     `json:"memory"` // 0..1
	Disk            *float64     `json:"disk"`   // 0..1, fullest real filesystem
	Load1           *float64     `json:"load1"`
	Load5           *float64     `json:"load5"`
	Load15          *float64     `json:"load15"`
	NetRx           *float64     `json:"net_rx"` // bytes/s
	NetTx           *float64     `json:"net_tx"`
	UptimeSeconds   *float64     `json:"uptime_seconds"`
	Cores           *float64     `json:"cores"`
	MemTotalBytes   *float64     `json:"memory_total_bytes"`
	MemUsedBytes    *float64     `json:"memory_used_bytes"`
	DiskTotalBytes  *float64     `json:"disk_total_bytes"`
	DiskUsedBytes   *float64     `json:"disk_used_bytes"`
	ExporterVersion string       `json:"exporter_version,omitempty"`
	Filesystems     []Filesystem `json:"filesystems,omitempty"`
}

// Snapshot is the state of the whole fleet at one instant, keyed by server_id.
type Snapshot struct {
	AsOf    time.Time
	Servers map[string]*Current
}

func f(v float64) *float64 { return &v }

// Snapshot runs the fleet-wide instant queries in parallel. If the `up` query
// fails, Prometheus is treated as unavailable and an error is returned.
// Other failing queries only leave their fields empty.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	now := time.Now().UTC()
	snap := &Snapshot{AsOf: now, Servers: map[string]*Current{}}
	var mu sync.Mutex
	get := func(id string) *Current {
		cur, ok := snap.Servers[id]
		if !ok {
			cur = &Current{}
			snap.Servers[id] = cur
		}
		return cur
	}

	type query struct {
		q     string
		apply func(id string, s Sample, cur *Current)
	}
	queries := []query{
		{`timestamp(up{job="node"})`, func(_ string, s Sample, cur *Current) {
			t := time.Unix(int64(s.Value), 0).UTC()
			cur.LastScrape = &t
		}},
		{cpuBusy(`job="node"`, "5m", "server_id"), func(_ string, s Sample, cur *Current) { cur.CPU = f(clamp01(s.Value)) }},
		{`count by (server_id) (node_cpu_seconds_total{job="node",mode="idle"})`, func(_ string, s Sample, cur *Current) { cur.Cores = f(s.Value) }},
		{`node_memory_MemTotal_bytes{job="node"}`, func(_ string, s Sample, cur *Current) { cur.MemTotalBytes = f(s.Value) }},
		{`node_memory_MemTotal_bytes{job="node"} - node_memory_MemAvailable_bytes{job="node"}`, func(_ string, s Sample, cur *Current) { cur.MemUsedBytes = f(s.Value) }},
		{`node_load1{job="node"}`, func(_ string, s Sample, cur *Current) { cur.Load1 = f(s.Value) }},
		{`node_load5{job="node"}`, func(_ string, s Sample, cur *Current) { cur.Load5 = f(s.Value) }},
		{`node_load15{job="node"}`, func(_ string, s Sample, cur *Current) { cur.Load15 = f(s.Value) }},
		{`sum by (server_id) (rate(node_network_receive_bytes_total{job="node",` + netFilter + `}[5m]))`, func(_ string, s Sample, cur *Current) { cur.NetRx = f(s.Value) }},
		{`sum by (server_id) (rate(node_network_transmit_bytes_total{job="node",` + netFilter + `}[5m]))`, func(_ string, s Sample, cur *Current) { cur.NetTx = f(s.Value) }},
		{`time() - node_boot_time_seconds{job="node"}`, func(_ string, s Sample, cur *Current) { cur.UptimeSeconds = f(s.Value) }},
		{`node_exporter_build_info{job="node"}`, func(_ string, s Sample, cur *Current) { cur.ExporterVersion = s.Labels["version"] }},
	}

	var upErr error
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		samples, err := c.Query(gctx, `up{job="node"}`, now)
		if err != nil {
			upErr = err
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		for _, s := range samples {
			if id := s.Labels["server_id"]; id != "" {
				up := s.Value == 1
				get(id).Up = &up
			}
		}
		return nil
	})
	for _, q := range queries {
		g.Go(func() error {
			samples, err := c.Query(gctx, q.q, now)
			if err != nil {
				return nil // partial data is acceptable; the field stays null
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range samples {
				if id := s.Labels["server_id"]; id != "" {
					q.apply(id, s, get(id))
				}
			}
			return nil
		})
	}
	// Filesystems need size and avail joined per mountpoint.
	var sizes, avails []Sample
	g.Go(func() error {
		s, err := c.Query(gctx, `node_filesystem_size_bytes{job="node",`+fsFilter+`}`, now)
		if err == nil {
			mu.Lock()
			sizes = s
			mu.Unlock()
		}
		return nil
	})
	g.Go(func() error {
		s, err := c.Query(gctx, `node_filesystem_avail_bytes{job="node",`+fsFilter+`}`, now)
		if err == nil {
			mu.Lock()
			avails = s
			mu.Unlock()
		}
		return nil
	})
	if err := g.Wait(); err != nil && upErr != nil {
		return nil, upErr
	}

	for id, fss := range joinFilesystems(sizes, avails) {
		cur := get(id)
		cur.Filesystems = fss
		var total, used, worst float64
		for _, fs := range fss {
			total += fs.SizeBytes
			used += fs.SizeBytes - fs.AvailBytes
			worst = max(worst, fs.UsedRatio)
		}
		if total > 0 {
			cur.DiskTotalBytes, cur.DiskUsedBytes, cur.Disk = f(total), f(used), f(worst)
		}
	}
	for _, cur := range snap.Servers {
		if cur.MemTotalBytes != nil && cur.MemUsedBytes != nil && *cur.MemTotalBytes > 0 {
			cur.Memory = f(clamp01(*cur.MemUsedBytes / *cur.MemTotalBytes))
		}
	}
	return snap, nil
}

// joinFilesystems pairs size and avail samples per (server, mountpoint), and
// drops repeated mounts of the same device so bind mounts are not double counted.
func joinFilesystems(sizes, avails []Sample) map[string][]Filesystem {
	type key struct{ id, mount string }
	avail := map[key]float64{}
	for _, s := range avails {
		avail[key{s.Labels["server_id"], s.Labels["mountpoint"]}] = s.Value
	}
	out := map[string][]Filesystem{}
	seenDev := map[[2]string]bool{}
	sort.Slice(sizes, func(i, j int) bool { return len(sizes[i].Labels["mountpoint"]) < len(sizes[j].Labels["mountpoint"]) })
	for _, s := range sizes {
		id, mount, dev := s.Labels["server_id"], s.Labels["mountpoint"], s.Labels["device"]
		a, ok := avail[key{id, mount}]
		if id == "" || !ok || s.Value <= 0 || seenDev[[2]string{id, dev}] {
			continue
		}
		seenDev[[2]string{id, dev}] = true
		out[id] = append(out[id], Filesystem{
			Mountpoint: mount, Device: dev, FSType: s.Labels["fstype"],
			SizeBytes: s.Value, AvailBytes: a, UsedRatio: clamp01(1 - a/s.Value),
		})
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool { return out[id][i].Mountpoint < out[id][j].Mountpoint })
	}
	return out
}

func clamp01(v float64) float64 { return max(0, min(1, v)) }

// cpuBusy is the share of CPU time not spent idle: 1 - idle / all modes.
// A ratio of two rates is used instead of avg(rate(idle)) because rate() over a
// window longer than a series' age underestimates; both rates are scaled the same
// way, so the ratio is right even for a server added a minute ago.
func cpuBusy(selector, window, by string) string {
	group := ""
	if by != "" {
		group = " by (" + by + ")"
	}
	return fmt.Sprintf(`1 - sum%[4]s (rate(node_cpu_seconds_total{%[1]s,mode="idle"}[%[2]s])) / sum%[4]s (rate(node_cpu_seconds_total{%[1]s}[%[2]s]))`,
		selector, window, "", group)
}

// Range is an allowed history window. Steps keep every series under ~500 points.
type Range struct {
	Name string
	Span time.Duration
	Step time.Duration
}

var Ranges = map[string]Range{
	"1h":  {"1h", time.Hour, 15 * time.Second},
	"6h":  {"6h", 6 * time.Hour, time.Minute},
	"24h": {"24h", 24 * time.Hour, 5 * time.Minute},
	"7d":  {"7d", 7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {"30d", 30 * 24 * time.Hour, 2 * time.Hour},
}

// HistorySeries is one chart line. Points are on a fixed grid; a missing sample is nil.
type HistorySeries struct {
	Name   string            `json:"name"`
	Unit   string            `json:"unit"`
	Labels map[string]string `json:"labels,omitempty"`
	Points [][2]any          `json:"points"` // [RFC3339 time, value or null]
}

// AllSeries are the series names a history request may ask for.
var AllSeries = []string{"cpu", "memory", "disk", "network", "load"}

// History returns the requested series for one server over r, ending now.
// coverage is the fraction of grid points that had data across all returned series.
func (c *Client) History(ctx context.Context, serverID string, r Range, series []string) ([]HistorySeries, float64, error) {
	s, err := sel(serverID)
	if err != nil {
		return nil, 0, err
	}
	end := time.Now().UTC().Truncate(r.Step)
	start := end.Add(-r.Span)
	win := fmt.Sprintf("%ds", int(max(5*time.Minute, 2*r.Step).Seconds()))

	type spec struct {
		name, unit, q string
		labelKeys     []string
	}
	var specs []spec
	for _, name := range series {
		switch name {
		case "cpu":
			specs = append(specs, spec{"cpu", "ratio", cpuBusy(s, win, ""), nil})
		case "memory":
			specs = append(specs, spec{"memory", "ratio", fmt.Sprintf(`1 - node_memory_MemAvailable_bytes{%s} / node_memory_MemTotal_bytes{%s}`, s, s), nil})
		case "disk":
			specs = append(specs, spec{"disk", "ratio", fmt.Sprintf(`1 - node_filesystem_avail_bytes{%s,%s} / node_filesystem_size_bytes{%s,%s}`, s, fsFilter, s, fsFilter), []string{"mountpoint"}})
		case "network":
			specs = append(specs,
				spec{"network_rx", "bytes_per_second", fmt.Sprintf(`sum(rate(node_network_receive_bytes_total{%s,%s}[%s]))`, s, netFilter, win), nil},
				spec{"network_tx", "bytes_per_second", fmt.Sprintf(`sum(rate(node_network_transmit_bytes_total{%s,%s}[%s]))`, s, netFilter, win), nil})
		case "load":
			for _, n := range []string{"1", "5", "15"} {
				specs = append(specs, spec{"load" + n, "load", fmt.Sprintf(`node_load%s{%s}`, n, s), nil})
			}
		}
	}

	results := make([][]HistorySeries, len(specs))
	g, gctx := errgroup.WithContext(ctx)
	for i, sp := range specs {
		g.Go(func() error {
			raw, err := c.QueryRange(gctx, sp.q, start, end, r.Step)
			if err != nil {
				return err
			}
			// Deduplicate repeated mounts of one device, as in the snapshot.
			seenDev := map[string]bool{}
			for _, rs := range raw {
				if dev := rs.Labels["device"]; sp.name == "disk" {
					if seenDev[dev] {
						continue
					}
					seenDev[dev] = true
				}
				hs := HistorySeries{Name: sp.name, Unit: sp.unit}
				for _, k := range sp.labelKeys {
					if hs.Labels == nil {
						hs.Labels = map[string]string{}
					}
					hs.Labels[k] = rs.Labels[k]
				}
				hs.Points = grid(start, end, r.Step, rs.Points)
				results[i] = append(results[i], hs)
			}
			if len(raw) == 0 { // no data at all: still return the grid, all null
				results[i] = append(results[i], HistorySeries{Name: sp.name, Unit: sp.unit, Points: grid(start, end, r.Step, nil)})
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, 0, err
	}
	var out []HistorySeries
	var have, total int
	for _, rs := range results {
		for _, hs := range rs {
			for _, p := range hs.Points {
				total++
				if p[1] != nil {
					have++
				}
			}
			out = append(out, hs)
		}
	}
	coverage := 0.0
	if total > 0 {
		coverage = float64(have) / float64(total)
	}
	return out, coverage, nil
}

// grid places points on the fixed step grid, leaving gaps as nil so charts never draw them as zero.
func grid(start, end time.Time, step time.Duration, pts []Point) [][2]any {
	byTime := make(map[int64]float64, len(pts))
	for _, p := range pts {
		byTime[p.Time.Unix()] = p.Value
	}
	var out [][2]any
	for t := start; !t.After(end); t = t.Add(step) {
		if v, ok := byTime[t.Unix()]; ok {
			out = append(out, [2]any{t.Format(time.RFC3339), v})
		} else {
			out = append(out, [2]any{t.Format(time.RFC3339), nil})
		}
	}
	return out
}

// Utilisation is a per-server measurement over a window.
type Utilisation struct {
	CPUP95    *float64
	MemoryP95 *float64
	UpSamples float64 // successful scrapes in the window
	AvgCPU    *float64
	AvgMemory *float64
	AvgDisk   *float64
}

// UtilisationOver returns p95 CPU/memory, averages and successful-scrape counts for
// every server over the window ending at end.
func (c *Client) UtilisationOver(ctx context.Context, window time.Duration, end time.Time) (map[string]*Utilisation, error) {
	w := fmt.Sprintf("%ds", int(window.Seconds()))
	cpu := "(" + cpuBusy(`job="node"`, "5m", "server_id") + ")"
	mem := `(1 - node_memory_MemAvailable_bytes{job="node"} / node_memory_MemTotal_bytes{job="node"})`
	disk := `(1 - sum by (server_id) (node_filesystem_avail_bytes{job="node",` + fsFilter + `}) / sum by (server_id) (node_filesystem_size_bytes{job="node",` + fsFilter + `}))`
	out := map[string]*Utilisation{}
	var mu sync.Mutex
	get := func(id string) *Utilisation {
		if out[id] == nil {
			out[id] = &Utilisation{}
		}
		return out[id]
	}
	qs := map[string]func(*Utilisation, float64){
		fmt.Sprintf(`quantile_over_time(0.95, %s[%s:5m])`, cpu, w): func(u *Utilisation, v float64) { u.CPUP95 = f(clamp01(v)) },
		fmt.Sprintf(`quantile_over_time(0.95, %s[%s:5m])`, mem, w): func(u *Utilisation, v float64) { u.MemoryP95 = f(clamp01(v)) },
		fmt.Sprintf(`avg_over_time(%s[%s:5m])`, cpu, w):            func(u *Utilisation, v float64) { u.AvgCPU = f(clamp01(v)) },
		fmt.Sprintf(`avg_over_time(%s[%s:5m])`, mem, w):            func(u *Utilisation, v float64) { u.AvgMemory = f(clamp01(v)) },
		fmt.Sprintf(`avg_over_time(%s[%s:5m])`, disk, w):           func(u *Utilisation, v float64) { u.AvgDisk = f(clamp01(v)) },
		fmt.Sprintf(`sum_over_time(up{job="node"}[%s])`, w):        func(u *Utilisation, v float64) { u.UpSamples = v },
	}
	g, gctx := errgroup.WithContext(ctx)
	for q, apply := range qs {
		g.Go(func() error {
			samples, err := c.Query(gctx, q, end)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for _, s := range samples {
				if id := s.Labels["server_id"]; id != "" {
					apply(get(id), s.Value)
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// DiskForecast lists filesystems predicted (linear fit over 6 h) to run out of
// space within the given horizon. A filesystem needs at least 2 h of history:
// a fit over a few minutes of writes extrapolates noise, not a trend.
func (c *Client) DiskForecast(ctx context.Context, horizon time.Duration) ([]Sample, error) {
	sel := fmt.Sprintf(`node_filesystem_avail_bytes{job="node",%s}`, fsFilter)
	return c.Query(ctx, fmt.Sprintf(
		`(predict_linear(%s[6h], %d) < 0) and on(server_id, mountpoint, device) ((time() - min_over_time(timestamp(%s)[6h:5m])) >= 7200)`,
		sel, int(horizon.Seconds()), sel), time.Time{})
}
