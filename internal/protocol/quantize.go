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
