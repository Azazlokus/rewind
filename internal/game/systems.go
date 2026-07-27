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
}

// Step продвигает одну сущность на dt секунд под вводом in.
//
// Это единственное определение движения игрока. Клиент запускает ту же функцию
// для предсказания (итерация 4), поэтому константы, порядок операций и
// округление float32 должны оставаться идентичными на обеих сторонах.
func Step(s *MoveState, in protocol.Input, dt float32) {
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
	s.VX = dx * PlayerSpeed
	s.VY = dy * PlayerSpeed
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
