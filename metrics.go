package main

// Tunnel Metrics: each running tunnel is its own process (a systemd
// service instance), separate from whichever process is showing the
// interactive menu — so a live in-memory counter here is invisible to that
// other process. Every tunnel instead snapshots its own traffic (and, for
// a kcp disguise, packet loss/FEC recovery — see kcp-go's own
// package-level DefaultSnmp) to a small JSON file next to its config every
// metricsSnapshotInterval; the menu screen just reads whichever files
// exist and renders them, the same "state lives on disk, not in a process
// only the menu can see" shape tunnels.go's own listTunnels already uses.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xtaci/kcp-go/v5"
)

const metricsSnapshotInterval = 15 * time.Second

// metricsSnapshot is what actually gets written to disk — deliberately
// flat and JSON-tagged rather than reusing any in-process struct, so this
// file's shape stays stable even if internal types change later.
type metricsSnapshot struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Mode      string    `json:"mode"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	BytesIn   int64     `json:"bytes_in"`
	BytesOut  int64     `json:"bytes_out"`

	// KCP-only — omitted (all zero, HasKCP false) for every other disguise.
	HasKCP      bool   `json:"has_kcp,omitempty"`
	KCPLostSegs uint64 `json:"kcp_lost_segs,omitempty"`
	KCPRetrans  uint64 `json:"kcp_retrans_segs,omitempty"`
	KCPFECRecov uint64 `json:"kcp_fec_recovered,omitempty"`
	KCPFECErrs  uint64 `json:"kcp_fec_errs,omitempty"`
	KCPInPkts   uint64 `json:"kcp_in_pkts,omitempty"`
	KCPOutPkts  uint64 `json:"kcp_out_pkts,omitempty"`
}

// metricsPath is the config path with its extension swapped for
// ".metrics.json" — kept alongside the config so it moves/deletes with it,
// and needs no separate directory or naming scheme of its own.
func metricsPath(configPath string) string {
	ext := filepath.Ext(configPath)
	return strings.TrimSuffix(configPath, ext) + ".metrics.json"
}

func configHasKCP(cfg *Config) bool {
	for _, d := range cfg.Disguise {
		if d.Enabled && d.Type == "kcp" {
			return true
		}
	}
	return false
}

// runMetricsSnapshotter periodically writes this process's own traffic (and
// KCP stats, if applicable) to disk — started from main.go alongside the
// existing statsLoop, for both roles, for the whole life of the process.
func runMetricsSnapshotter(ctx context.Context, cfg *Config, configPath, role string) {
	name := strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
	path := metricsPath(configPath)
	hasKCP := configHasKCP(cfg)
	startedAt := time.Now()

	write := func() {
		snap := metricsSnapshot{
			Name:      name,
			Role:      role,
			Mode:      cfg.Mode,
			StartedAt: startedAt,
			UpdatedAt: time.Now(),
			BytesIn:   atomic.LoadInt64(&metricsBytesIn),
			BytesOut:  atomic.LoadInt64(&metricsBytesOut),
		}
		if hasKCP {
			s := kcp.DefaultSnmp.Copy()
			snap.HasKCP = true
			snap.KCPLostSegs = s.LostSegs
			snap.KCPRetrans = s.RetransSegs
			snap.KCPFECRecov = s.FECRecovered
			snap.KCPFECErrs = s.FECErrs
			snap.KCPInPkts = s.InPkts
			snap.KCPOutPkts = s.OutPkts
		}
		data, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			return
		}
		// Best-effort: a metrics file the menu can't read yet just shows as
		// "no data" for one cycle, never worth failing the tunnel over.
		tmp := path + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			os.Rename(tmp, path)
		}
	}

	write() // first snapshot as soon as possible, not after the first interval
	ticker := time.NewTicker(metricsSnapshotInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			write() // final snapshot so "tunnel up for" freezes at the real stop time
			return
		case <-ticker.C:
			write()
		}
	}
}

func loadMetrics(configPath string) (metricsSnapshot, bool) {
	data, err := os.ReadFile(metricsPath(configPath))
	if err != nil {
		return metricsSnapshot{}, false
	}
	var snap metricsSnapshot
	if json.Unmarshal(data, &snap) != nil {
		return metricsSnapshot{}, false
	}
	return snap, true
}

// menuTunnelMetrics renders every tunnel's last snapshot — "recorded Ns
// ago" makes a stale file (the tunnel stopped, or was never started since
// upgrading to a build with this feature) obvious at a glance rather than
// silently showing a frozen, misleadingly-current-looking number.
func menuTunnelMetrics(tunnels []tunnelRef) {
	sectionHeader("Tunnel Metrics")
	fmt.Println(dim("Measured from the traffic each tunnel actually carried."))
	fmt.Println()

	found := false
	for _, t := range tunnels {
		if t.isPaqet() {
			continue // paqet has its own logs/stats — not this project's traffic counters
		}
		snap, ok := loadMetrics(t.Path)
		if !ok {
			continue
		}
		found = true

		fmt.Println(bold(t.Name) + "  " + dim(t.Role+" / "+transportLabelShort(snap.Mode)))
		age := time.Since(snap.UpdatedAt).Round(time.Second)
		staleness := dim(fmt.Sprintf("recorded %s ago", age))
		if age > 2*metricsSnapshotInterval {
			staleness = yellow(fmt.Sprintf("recorded %s ago — tunnel may not be running", age))
		}
		fmt.Printf("  %s, tunnel up for %s\n", staleness, snap.UpdatedAt.Sub(snap.StartedAt).Round(time.Second))
		fmt.Printf("  Traffic       : %s in, %s out\n", humanBytes(snap.BytesIn), humanBytes(snap.BytesOut))
		if snap.HasKCP {
			lossPct := 0.0
			if total := snap.KCPInPkts + snap.KCPLostSegs; total > 0 {
				lossPct = 100 * float64(snap.KCPLostSegs) / float64(total)
			}
			fmt.Printf("  Packet loss   : %.1f%%  (%d lost, %d retransmitted)\n", lossPct, snap.KCPLostSegs, snap.KCPRetrans)
			fmt.Printf("  FEC           : %d packets recovered, %d recovery errors\n", snap.KCPFECRecov, snap.KCPFECErrs)
		}
		fmt.Println()
	}

	if !found {
		fmt.Println(dim("no metrics recorded yet — a tunnel needs to be running a build with"))
		fmt.Println(dim("this feature for at least " + metricsSnapshotInterval.String() + " to produce its first snapshot."))
	}
	pressEnter()
}

// transportLabelShort is transportLabel without the disguise-type suffix —
// this screen already prints the role next to it, so "TCP Mux" reads
// cleaner here than "TCP Mux (kcp)".
func transportLabelShort(mode string) string {
	switch mode {
	case "tcp":
		return "TCP"
	case "tcpmux":
		return "TCP Mux"
	case "udp":
		return "UDP raw"
	default:
		return mode
	}
}
