package inventory

import (
	"time"

	"bm-monitor/api/internal/metrics"
)

// Server status values (plan section 11.1).
const (
	StatusOnline   = "online"
	StatusOffline  = "offline"
	StatusUnknown  = "unknown"
	StatusDisabled = "disabled"
)

// Status derives a server's status from the database row and the metrics snapshot.
//
//	disabled  enabled = false (not scraped)
//	unknown   no sample yet, Prometheus unavailable, or the last scrape is older than 2× the interval
//	offline   the latest scrape failed (up == 0)
//	online    up == 1 and the last scrape is fresh
func Status(s *Server, cur *metrics.Current, now time.Time) string {
	if !s.Enabled {
		return StatusDisabled
	}
	if cur == nil || cur.Up == nil || cur.LastScrape == nil {
		return StatusUnknown
	}
	if now.Sub(*cur.LastScrape) > 2*time.Duration(s.ScrapeIntervalS)*time.Second+5*time.Second {
		return StatusUnknown
	}
	if !*cur.Up {
		return StatusOffline
	}
	return StatusOnline
}
