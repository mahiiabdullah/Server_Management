package metrics

import (
	"strings"
	"testing"
)

// Regression: avg(rate(idle)) under-reads idle time for series younger than the
// window, showing ~25% CPU on idle servers just after they were added. CPU must be
// a ratio of idle to all CPU time so that bias cancels.
func TestCPUBusyIsARatioOfRates(t *testing.T) {
	q := cpuBusy(`job="node"`, "5m", "server_id")
	want := `1 - sum by (server_id) (rate(node_cpu_seconds_total{job="node",mode="idle"}[5m])) / sum by (server_id) (rate(node_cpu_seconds_total{job="node"}[5m]))`
	if q != want {
		t.Fatalf("got  %s\nwant %s", q, want)
	}
	if strings.Contains(cpuBusy(`server_id="x"`, "300s", ""), " by (") {
		t.Error("ungrouped expression must not group")
	}
}
