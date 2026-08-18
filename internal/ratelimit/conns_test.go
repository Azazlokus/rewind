package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestConnLimiterDisabled(t *testing.T) {
	for _, max := range []int{0, -1} {
		if c := NewConnLimiter(max); c != nil {
			t.Fatalf("NewConnLimiter(%d) should disable (got non-nil)", max)
		}
	}
	// nil-приёмник всё пропускает; Release — no-op (не паникует).
	var c *ConnLimiter
	if !c.Acquire("x") {
		t.Fatal("nil ConnLimiter Acquire should allow")
	}
	c.Release("x")
}

func TestConnLimiterCap(t *testing.T) {
	c := NewConnLimiter(2)
	if !c.Acquire("ip") {
		t.Fatal("first acquire should succeed (cap 2)")
	}
	if !c.Acquire("ip") {
		t.Fatal("second acquire should succeed (cap 2)")
	}
	if c.Acquire("ip") {
		t.Fatal("third acquire should fail — cap reached")
	}
	c.Release("ip") // освободили один слот
	if !c.Acquire("ip") {
		t.Fatal("acquire after release should succeed")
	}
}

func TestConnLimiterPerKeyIsolation(t *testing.T) {
	c := NewConnLimiter(1)
	if !c.Acquire("A") {
		t.Fatal("A first acquire should succeed")
	}
	if c.Acquire("A") {
		t.Fatal("A second acquire should fail (cap 1)")
	}
	if !c.Acquire("B") {
		t.Fatal("B has its own counter, should succeed")
	}
}

// TestConnLimiterReleaseBounded: после освобождения всех слотов ключ исчезает из
// карты — она не растёт без предела.
func TestConnLimiterReleaseBounded(t *testing.T) {
	c := NewConnLimiter(4)
	c.Acquire("ip")
	c.Acquire("ip")
	c.Release("ip")
	c.Release("ip")

	c.mu.Lock()
	_, present := c.count["ip"]
	c.mu.Unlock()
	if present {
		t.Fatal("key with zero live slots should be evicted from the map")
	}
}

// TestConnLimiterConcurrent: под гонкой число одновременно занятых слотов НИКОГДА не
// превышает max, и всё чисто сходится к нулю. -race ловит гонки по карте.
func TestConnLimiterConcurrent(t *testing.T) {
	const max = 8
	c := NewConnLimiter(max)

	var live atomic.Int64 // сколько слотов занято прямо сейчас
	var peak atomic.Int64 // наблюдённый максимум
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if !c.Acquire("same") {
					continue
				}
				n := live.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				live.Add(-1)
				c.Release("same")
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > max {
		t.Fatalf("live slots peaked at %d, must never exceed cap %d", p, max)
	}
	if n := live.Load(); n != 0 {
		t.Fatalf("live count should settle at 0, got %d", n)
	}
	c.mu.Lock()
	remaining := c.count["same"]
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("counter for key should be 0 after all releases, got %d", remaining)
	}
}
