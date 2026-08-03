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
	// AntiCheat считает n событий вида kind (метка Prometheus, из AntiCheatKind.String),
	// зажатых/отклонённых серверной античит-проверкой (итерация 25). Наблюдаемость, не
	// решение — сервер и без метрики авторитетно зажимает; счётчик лишь делает попытки
	// видимыми оператору. Метка — строка, чтобы metrics не импортировал game.
	AntiCheat(kind string, n int)
}

// AntiCheatKind классифицирует подозрительное или зажатое серверной проверкой
// событие (итерация 25) для метрик. Это НАБЛЮДЕНИЕ: симуляция от него не зависит,
// в Checksum не входит и в лог реплея не пишется — античит уже выражен в самих
// клампах (clampRewind и т.п.), метрика лишь считает их срабатывания.
type AntiCheatKind uint8

const (
	// ACRewindStale — клиент прислал ViewTick дальше окна перемотки в прошлое
	// (now-ViewTick > maxRewindTicks): сдвиг зажат до потолка. Высокая задержка,
	// артефакт интерполяции или lag-switch.
	ACRewindStale AntiCheatKind = iota
	// ACRewindFuture — ViewTick из будущего (now-ViewTick < 0): рассинхрон часов
	// или подмена времени клиентом. Сдвиг зажат до 0.
	ACRewindFuture
	// antiCheatKindCount — размер массива счётчиков (держать последним).
	antiCheatKindCount
)

// String даёт стабильную метку для Prometheus (значение лейбла).
func (k AntiCheatKind) String() string {
	switch k {
	case ACRewindStale:
		return "rewind_stale"
	case ACRewindFuture:
		return "rewind_future"
	default:
		return "unknown"
	}
}

// NopRecorder отбрасывает телеметрию. Значение по умолчанию для комнат в тестах.
type NopRecorder struct{}

func (NopRecorder) TickDuration(time.Duration) {}
func (NopRecorder) SnapshotBytes(int)          {}
func (NopRecorder) EntitiesPerSnapshot(int)    {}
func (NopRecorder) ConnectedPlayers(int)       {}
func (NopRecorder) InboxDepth(int)             {}
func (NopRecorder) AntiCheat(string, int)      {}
