// Пакет metrics выставляет счётчики и гистограммы Prometheus для сервера.
//
// Он реализует game.Recorder, поэтому комната отчитывается в него, не импортируя
// сам Prometheus. Горячий путь трогает только lock-free атомарные счётчики
// внутри клиентской библиотеки, так что запись тика никогда не блокирует game
// loop.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics — реестр Prometheus сервера и его инструменты.
type Metrics struct {
	reg *prometheus.Registry

	tickDuration        prometheus.Histogram
	snapshotBytes       prometheus.Counter
	entitiesPerSnapshot prometheus.Histogram
	connectedPlayers    prometheus.Gauge
	inboxDepth          prometheus.Gauge
	activeBots          prometheus.Gauge
	antiCheat           *prometheus.CounterVec
}

// New строит Metrics с собственным реестром, чтобы тесты могли создавать
// несколько, не сталкиваясь на глобальном реестре по умолчанию.
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		tickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "arena_tick_duration_seconds",
			Help: "Wall-clock duration of one simulation tick.",
			// Корзины охватывают целевой p99 в 15 мс из итерации 6.
			Buckets: []float64{
				0.0005, 0.001, 0.002, 0.004, 0.008,
				0.015, 0.020, 0.030, 0.050, 0.100,
			},
		}),
		snapshotBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "arena_snapshot_bytes_total",
			Help: "Total bytes queued towards clients in snapshots.",
		}),
		entitiesPerSnapshot: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "arena_entities_per_snapshot",
			Help: "Entities in one snapshot sent to a client; bounded by interest management (iteration 6).",
			// Корзины охватывают вид AOI (десятки) и полный мир (до предела провода).
			Buckets: []float64{1, 2, 4, 8, 16, 32, 48, 64, 96, 128, 192, 255},
		}),
		connectedPlayers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "arena_connected_players",
			Help: "Players currently connected across all rooms.",
		}),
		inboxDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "arena_inbox_depth",
			Help: "Room inbox depth sampled after the last tick.",
		}),
		activeBots: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "arena_active_bots",
			Help: "AI bots currently kept in rooms by the filler (iteration 17).",
		}),
		antiCheat: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "arena_anticheat_events_total",
			Help: "Server-side anti-cheat clamps/rejections by kind (iteration 25).",
		}, []string{"kind"}),
	}
	m.reg.MustRegister(m.tickDuration, m.snapshotBytes, m.entitiesPerSnapshot, m.connectedPlayers, m.inboxDepth, m.activeBots, m.antiCheat)
	return m
}

// Handler отдаёт реестр на /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Методы ниже удовлетворяют game.Recorder.

func (m *Metrics) TickDuration(d time.Duration) { m.tickDuration.Observe(d.Seconds()) }
func (m *Metrics) SnapshotBytes(n int)          { m.snapshotBytes.Add(float64(n)) }
func (m *Metrics) EntitiesPerSnapshot(n int)    { m.entitiesPerSnapshot.Observe(float64(n)) }
func (m *Metrics) ConnectedPlayers(n int)       { m.connectedPlayers.Set(float64(n)) }
func (m *Metrics) InboxDepth(n int)             { m.inboxDepth.Set(float64(n)) }
func (m *Metrics) AntiCheat(kind string, n int) { m.antiCheat.WithLabelValues(kind).Add(float64(n)) }

// ActiveBots публикует число ботов, которых наполнитель держит в комнатах. Не часть
// game.Recorder (комната про ботов не знает) — зовётся горутиной наполнителя.
func (m *Metrics) ActiveBots(n int) { m.activeBots.Set(float64(n)) }
