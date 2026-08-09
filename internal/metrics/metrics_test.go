package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue читает текущее значение счётчика через dto.Metric — без внешнего
// testutil, чтобы не тянуть лишнюю зависимость в модуль.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetCounter().GetValue()
}

// TestAntiCheatCounter: AntiCheat увеличивает счётчик по метке kind, метки
// независимы (итерация 25).
func TestAntiCheatCounter(t *testing.T) {
	m := New()
	m.AntiCheat("rewind_stale", 2)
	m.AntiCheat("rewind_stale", 3)
	m.AntiCheat("rewind_future", 1)

	if got := counterValue(t, m.antiCheat.WithLabelValues("rewind_stale")); got != 5 {
		t.Fatalf("rewind_stale = %v, want 5", got)
	}
	if got := counterValue(t, m.antiCheat.WithLabelValues("rewind_future")); got != 1 {
		t.Fatalf("rewind_future = %v, want 1", got)
	}
}
