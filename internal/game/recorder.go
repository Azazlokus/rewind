package game

import "time"

// Recorder принимает телеметрию от комнаты. Реализации обязаны быть безопасны
// для конкурентного использования; в internal/metrics лежит Prometheus-вариант.
//
// Интерфейс живёт здесь, рядом с единственным вызывающим, чтобы симуляция не
// имела зависимостей от стека метрик.
type Recorder interface {
	// TickDuration фиксирует, сколько занял один шаг симуляции.
	TickDuration(d time.Duration)
	// SnapshotBytes считает байты, поставленные в очередь к клиентам.
	SnapshotBytes(n int)
	// EntitiesPerSnapshot фиксирует число сущностей в одном отправленном снапшоте.
	// При interest management (итерация 6) оно перестаёт расти с размером комнаты —
	// эта метрика делает эффект наблюдаемым.
	EntitiesPerSnapshot(n int)
	// ConnectedPlayers сообщает текущее число игроков в комнате.
	ConnectedPlayers(n int)
	// InboxDepth сообщает, сколько событий осталось в очереди после тика.
	InboxDepth(n int)
}

// NopRecorder отбрасывает телеметрию. Значение по умолчанию для комнат в тестах.
type NopRecorder struct{}

func (NopRecorder) TickDuration(time.Duration) {}
func (NopRecorder) SnapshotBytes(int)          {}
func (NopRecorder) EntitiesPerSnapshot(int)    {}
func (NopRecorder) ConnectedPlayers(int)       {}
func (NopRecorder) InboxDepth(int)             {}
