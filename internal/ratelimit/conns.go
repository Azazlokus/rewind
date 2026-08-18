package ratelimit

import "sync"

// ConnLimiter ограничивает число ОДНОВРЕМЕННО занятых слотов на ключ (например,
// живых игровых соединений с одного IP). В отличие от Limiter (ограничение
// скорости) это счётчик-гейдж: Acquire занимает слот, Release освобождает.
// Конкурентно-безопасен (mu защищает карту счётчиков). Карта не растёт без предела:
// ключ с нулём занятых слотов удаляется.
type ConnLimiter struct {
	max   int
	mu    sync.Mutex
	count map[string]int
}

// NewConnLimiter собирает кап на max одновременных слотов на ключ. max <= 0 → nil
// (кап выключен; методы на nil-приёмнике всё пропускают — вызывающему не нужен
// nil-чек).
func NewConnLimiter(max int) *ConnLimiter {
	if max <= 0 {
		return nil
	}
	return &ConnLimiter{max: max, count: make(map[string]int)}
}

// Acquire пытается занять слот для key. Возвращает true и занимает слот, если у
// ключа меньше max занятых; иначе false БЕЗ занятия (тогда Release звать НЕ нужно).
// На nil-приёмнике (кап выключен) всегда true. Каждому успешному Acquire обязан
// соответствовать ровно один Release.
func (c *ConnLimiter) Acquire(key string) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count[key] >= c.max {
		return false
	}
	c.count[key]++
	return true
}

// Release освобождает слот, ранее занятый успешным Acquire. Обнулившийся ключ
// удаляется из карты. На nil-приёмнике — no-op.
func (c *ConnLimiter) Release(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.count[key]; n > 1 {
		c.count[key] = n - 1
	} else {
		delete(c.count, key) // n <= 1: последний слот освобождён (или защита от рассинхрона)
	}
}
