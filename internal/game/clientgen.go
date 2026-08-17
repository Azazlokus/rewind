package game

// Поверхность генератора клиента (итер. 41). Симуляционные константы и статичная
// геометрия арены, которые зеркалит web-клиент (web/game.js): движение и рывок он
// предсказывает тем же шагом, а геометрию (стены, точки пикапов/зон, базы) рисует и
// использует для коллизии — значения обязаны совпадать с сервером, иначе предсказание
// дрейфит, а рендер расходится. Раньше это зеркалилось руками; теперь константы ниже —
// источник истины, а cmd/genclient генерирует из них блок в web/game.js.
//
// Это read-only снимок уже существующих констант/раскладок пакета; на симуляцию,
// состояние World и Checksum он не влияет (как Obstacles из итер. 28).

// GenVec2 — точка в мировых координатах для генератора.
type GenVec2 struct{ X, Y float32 }

// GenAABB — коробка стены для генератора.
type GenAABB struct{ MinX, MinY, MaxX, MaxY float32 }

// SimConstants — снимок симуляционных/геометрических констант, зеркалимых клиентом.
type SimConstants struct {
	MapSize, PlayerRadius, PlayerSpeed, ProjectileRadius float32
	InvSqrt2                                             float32
	DashSpeedMult, DashDuration, DashCooldown            float32
	HillX, HillY, HillRadius                             float32
	DomRadius                                            float32
	DomPoints                                            []GenVec2
	FlagBaseRadius                                       float32
	FlagBases                                            []GenVec2
	Walls                                                []GenAABB
	PickupSpots                                          []GenVec2
	// InputRate — частота клиентского ввода (Гц); клиент считает PREDICT.dt = 1/InputRate.
	InputRate int
	// Значения enum'ов, зеркалимые в PROTO клиента.
	MatchActive, MatchIntermission                          uint8
	PickupMedkit, PickupRapid, PickupSpread                 uint8
	WeaponPistol, WeaponShotgun, WeaponSniper, WeaponRocket uint8
}

// ClientSim возвращает симуляционные константы для генератора клиента (cmd/genclient).
func ClientSim() SimConstants {
	sc := SimConstants{
		MapSize:          MapSize,
		PlayerRadius:     PlayerRadius,
		PlayerSpeed:      PlayerSpeed,
		ProjectileRadius: ProjectileRadius,
		InvSqrt2:         invSqrt2,
		DashSpeedMult:    dashSpeedMult,
		DashDuration:     dashDurationSec,
		DashCooldown:     dashCooldownSec,
		HillX:            hillX,
		HillY:            hillY,
		HillRadius:       hillRadius,
		DomRadius:        domRadius,
		FlagBaseRadius:   flagBaseRadius,

		InputRate: InputRate,

		MatchActive:       uint8(matchActive),
		MatchIntermission: uint8(matchIntermission),
		PickupMedkit:      uint8(pickupMedkit),
		PickupRapid:       uint8(pickupRapid),
		PickupSpread:      uint8(pickupSpread),

		WeaponPistol:  uint8(weaponPistol),
		WeaponShotgun: uint8(weaponShotgun),
		WeaponSniper:  uint8(weaponSniper),
		WeaponRocket:  uint8(weaponRocket),
	}
	for _, p := range domPoints {
		sc.DomPoints = append(sc.DomPoints, GenVec2{X: p.x, Y: p.y})
	}
	for _, b := range flagBases {
		sc.FlagBases = append(sc.FlagBases, GenVec2{X: b.x, Y: b.y})
	}
	for _, w := range walls {
		sc.Walls = append(sc.Walls, GenAABB{MinX: w.minX, MinY: w.minY, MaxX: w.maxX, MaxY: w.maxY})
	}
	for _, s := range pickupSpots {
		sc.PickupSpots = append(sc.PickupSpots, GenVec2{X: s.x, Y: s.y})
	}
	return sc
}
