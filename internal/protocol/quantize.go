package protocol

import "math"

// Квантование позиций и скоростей для провода. Координаты хранятся как uint16 с
// шагом 1/CoordScale юнита, скорости — как int16 с тем же шагом. Это единственное
// место, где определяется потеря точности на проводе; тесты round-trip сверяются
// с этими же функциями.

// quantizeCoord переводит координату в [0, MapSize) в uint16 с шагом
// 1/CoordScale. Значения за пределами диапазона прижимаются, а не заворачиваются.
func quantizeCoord(v float32) uint16 {
	q := math.Round(float64(v) * CoordScale)
	if q < 0 {
		return 0
	}
	if q > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(q)
}

// dequantizeCoord — обратная операция к quantizeCoord.
func dequantizeCoord(q uint16) float32 {
	return float32(q) / CoordScale
}

// quantizeVel переводит компоненту скорости в int16 с шагом 1/CoordScale,
// прижимая к диапазону int16.
func quantizeVel(v float32) int16 {
	q := math.Round(float64(v) * CoordScale)
	if q < math.MinInt16 {
		return math.MinInt16
	}
	if q > math.MaxInt16 {
		return math.MaxInt16
	}
	return int16(q)
}

// dequantizeVel — обратная операция к quantizeVel.
func dequantizeVel(q int16) float32 {
	return float32(q) / CoordScale
}

// EntityFieldMask возвращает битовую маску полей (kind/x/y/vx/vy/hp), которыми cur
// отличается от base в проводном (квантованном) представлении; 0 — записи
// проводно-идентичны. На этом стоит field-level дельта (итерация 9): в изменённой
// записи шлются только помеченные поля. Сравнение по квантованным значениям, а не
// по сырым float, чтобы субквантовое дрожание не порождало ложных изменений. id не
// сравнивается — он идёт в записи отдельно и всегда присутствует.
func EntityFieldMask(base, cur Entity) uint8 {
	var m uint8
	if base.Kind != cur.Kind {
		m |= FieldKind
	}
	if quantizeCoord(base.X) != quantizeCoord(cur.X) {
		m |= FieldX
	}
	if quantizeCoord(base.Y) != quantizeCoord(cur.Y) {
		m |= FieldY
	}
	if quantizeVel(base.VX) != quantizeVel(cur.VX) {
		m |= FieldVX
	}
	if quantizeVel(base.VY) != quantizeVel(cur.VY) {
		m |= FieldVY
	}
	if base.HP != cur.HP {
		m |= FieldHP
	}
	return m
}
