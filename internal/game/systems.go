package game

import "arena/internal/protocol"

// Константы движения. Клиент зеркалит эти значения в web/game.js; изменение
// только одной стороны проявится как дрейф предсказания, а не как ошибка
// компиляции.
const (
	// PlayerSpeed — скорость движения в мировых юнитах в секунду.
	PlayerSpeed float32 = 300
	// PlayerRadius — радиус столкновения игрока в мировых юнитах.
	PlayerRadius float32 = 16
	// MapSize — сторона квадратной карты в мировых юнитах.
	MapSize float32 = protocol.MapSize
	// invSqrt2 нормализует диагональное движение.
	invSqrt2 float32 = 0.70710678

	// Рывок (итер. 27): короткий рывок-ускорение в сторону движения по действию
	// ActDash. Клиент зеркалит эти константы (web/game.js SIM.Dash*) — рывок
	// предсказывается, поэтому значения, порядок и клэмп обязаны совпадать.
	// dashSpeedMult — во сколько раз ускоряется движение на время рывка.
	dashSpeedMult float32 = 2.6
	// dashDurationSec — длительность рывка-ускорения, секунды (~0.18 с → ~140 юнитов).
	dashDurationSec float32 = 0.18
	// dashCooldownSec — задержка между рывками, секунды.
	dashCooldownSec float32 = 2.5
)

// InputRate — частота клиентского ввода (Гц), зафиксированное решение (тик 30,
// ввод 60). Каждый ввод представляет 1/InputRate секунды симуляции: сервер
// применяет вводы из очереди этим шагом (World.Step), а клиент предсказывает тем
// же (web/game.js: PREDICT.dt = 1/InputRate). Обе стороны держать согласованными,
// иначе предсказание дрейфит.
const InputRate = 60

// inputDt — шаг интегрирования одного клиентского ввода.
const inputDt float32 = 1.0 / InputRate

// MoveState — часть сущности, которую трогает общий шаг движения.
type MoveState struct {
	X, Y   float32
	VX, VY float32
	// Таймеры рывка (итер. 27), в секундах: dashCD — до следующего доступного рывка,
	// dashT — сколько ещё длится текущее рывок-ускорение. Живут здесь (а не в Player),
	// потому что их трогает общий Step и зеркалит клиентское предсказание. Входят в
	// Checksum. Оба спадают на dt каждый шаг.
	dashCD, dashT float32
}

// Step продвигает одну сущность на dt секунд под вводом in.
//
// Это единственное определение движения игрока. Клиент запускает ту же функцию
// для предсказания (итерация 4), поэтому константы, порядок операций и
// округление float32 должны оставаться идентичными на обеих сторонах.
func Step(s *MoveState, in protocol.Input, dt float32) {
	// Таймеры рывка спадают на dt (итер. 27); ниже — триггер и множитель скорости.
	if s.dashCD > 0 {
		s.dashCD -= dt
		if s.dashCD < 0 {
			s.dashCD = 0
		}
	}
	if s.dashT > 0 {
		s.dashT -= dt
		if s.dashT < 0 {
			s.dashT = 0
		}
	}

	var dx, dy float32
	if in.Buttons&protocol.BtnLeft != 0 {
		dx -= 1
	}
	if in.Buttons&protocol.BtnRight != 0 {
		dx += 1
	}
	if in.Buttons&protocol.BtnUp != 0 {
		dy -= 1
	}
	if in.Buttons&protocol.BtnDown != 0 {
		dy += 1
	}
	if dx != 0 && dy != 0 {
		dx *= invSqrt2
		dy *= invSqrt2
	}
	// Рывок стартует только при движении и снятом кулдауне (итер. 27): даёт ускорение
	// в текущую сторону движения на dashDurationSec и уходит в кулдаун.
	if in.Action(protocol.ActDash) && s.dashCD <= 0 && (dx != 0 || dy != 0) {
		s.dashT = dashDurationSec
		s.dashCD = dashCooldownSec
	}
	speed := PlayerSpeed
	if s.dashT > 0 {
		speed = PlayerSpeed * dashSpeedMult
	}
	s.VX = dx * speed
	s.VY = dy * speed
	nx := clamp(s.X+s.VX*dt, PlayerRadius, MapSize-PlayerRadius)
	ny := clamp(s.Y+s.VY*dt, PlayerRadius, MapSize-PlayerRadius)
	// Коллизия со статичными стенами: круг радиуса PlayerRadius выталкивается из
	// препятствий (итерация 10). Внутри общего Step — значит клиент повторяет её при
	// предсказании тем же кодом (web/game.js: resolveWalls в stepMove).
	s.X, s.Y = resolveWalls(nx, ny, PlayerRadius)
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
