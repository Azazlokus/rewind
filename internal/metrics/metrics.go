// Package metrics exposes Prometheus counters and histograms for the server.
//
// It implements game.Recorder so a room reports into it without importing
// Prometheus itself. The hot path only touches lock-free atomic counters inside
// the client library, so recording a tick never blocks the game loop.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the server's Prometheus registry and instruments.
type Metrics struct {
	reg *prometheus.Registry

	tickDuration     prometheus.Histogram
	snapshotBytes    prometheus.Counter
	connectedPlayers prometheus.Gauge
	inboxDepth       prometheus.Gauge
}

// New builds a Metrics with its own registry, so tests can create several
// without colliding on the global default registry.
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		tickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "arena_tick_duration_seconds",
			Help: "Wall-clock duration of one simulation tick.",
			// Buckets straddle the 15 ms p99 target of iteration 6.
			Buckets: []float64{
				0.0005, 0.001, 0.002, 0.004, 0.008,
				0.015, 0.020, 0.030, 0.050, 0.100,
			},
		}),
		snapshotBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "arena_snapshot_bytes_total",
			Help: "Total bytes queued towards clients in snapshots.",
		}),
		connectedPlayers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "arena_connected_players",
			Help: "Players currently connected across all rooms.",
		}),
		inboxDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "arena_inbox_depth",
			Help: "Room inbox depth sampled after the last tick.",
		}),
	}
	m.reg.MustRegister(m.tickDuration, m.snapshotBytes, m.connectedPlayers, m.inboxDepth)
	return m
}

// Handler serves the registry at /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// The four methods below satisfy game.Recorder.

func (m *Metrics) TickDuration(d time.Duration) { m.tickDuration.Observe(d.Seconds()) }
func (m *Metrics) SnapshotBytes(n int)          { m.snapshotBytes.Add(float64(n)) }
func (m *Metrics) ConnectedPlayers(n int)       { m.connectedPlayers.Set(float64(n)) }
func (m *Metrics) InboxDepth(n int)             { m.inboxDepth.Set(float64(n)) }
