package game

import (
	"sync"
	"time"
)

// Clock — единственный источник времени комнаты. Прод использует RealClock;
// тесты — ManualClock, прогоняя тысячу тиков за микросекунды, детерминированно и
// без единого time.Sleep.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker — подмножество time.Ticker, нужное комнате.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock — реализация Clock по настенным часам.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) NewTicker(d time.Duration) Ticker {
	return realTicker{t: time.NewTicker(d)}
}

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// ManualClock — Clock, время которого движется, только когда так скажет тест.
//
// Advance доставляет тики синхронно: возвращается только после того, как
// потребитель получил каждый выпущенный тик. Поэтому тест может продвинуть часы,
// затем прочитать полученный снапшот и быть уверенным в их порядке. Если тик
// никто не потребляет, Advance блокируется до срабатывания таймаута самого теста.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*manualTicker
}

// NewManualClock возвращает часы, стартующие с t. Нулевое t стартует с
// фиксированной произвольной даты, чтобы логи в тестах выглядели вменяемо.
func NewManualClock(t time.Time) *ManualClock {
	if t.IsZero() {
		t = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &ManualClock{now: t}
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("game: ManualClock.NewTicker with non-positive period")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTicker{
		c:       make(chan time.Time),
		period:  d,
		next:    c.now.Add(d),
		stopped: make(chan struct{}),
	}
	c.tickers = append(c.tickers, t)
	return t
}

// Advance двигает время вперёд на d, выпуская каждый подошедший тикер в
// хронологическом порядке.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	deadline := c.now.Add(d)
	c.mu.Unlock()

	for {
		c.mu.Lock()
		var due *manualTicker
		var at time.Time
		for _, t := range c.tickers {
			if t.isStopped() || t.next.After(deadline) {
				continue
			}
			if due == nil || t.next.Before(at) {
				due, at = t, t.next
			}
		}
		if due == nil {
			c.now = deadline
			c.mu.Unlock()
			return
		}
		c.now = at
		due.next = at.Add(due.period)
		ch := due.c
		c.mu.Unlock()

		// Отправляем вне блокировки: потребитель зовёт Now() при обработке тика.
		select {
		case ch <- at:
		case <-due.stopped:
		}
	}
}

// AdvanceTicks продвигает часы на n периодов по d.
func (c *ManualClock) AdvanceTicks(n int, d time.Duration) {
	for range n {
		c.Advance(d)
	}
}

type manualTicker struct {
	c       chan time.Time
	period  time.Duration
	next    time.Time // под защитой ManualClock.mu
	stopped chan struct{}
	once    sync.Once
}

func (t *manualTicker) C() <-chan time.Time { return t.c }

func (t *manualTicker) Stop() { t.once.Do(func() { close(t.stopped) }) }

func (t *manualTicker) isStopped() bool {
	select {
	case <-t.stopped:
		return true
	default:
		return false
	}
}
