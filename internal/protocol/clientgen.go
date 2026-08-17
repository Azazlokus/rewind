package protocol

// Поверхность генератора клиента (итер. 41). Web-клиент (web/game.js) обязан
// зеркалить константы провода один-в-один — раньше это делалось руками и было
// источником дрейфа (напр. сломанный разбор MsgMatchState в итер. 23). Теперь
// значения ниже — единственный источник истины, а cmd/genclient генерирует из них
// соответствующий блок в web/game.js. Функция читает РЕАЛЬНЫЕ значения констант
// (в т.ч. на iota и неэкспортированные) в этом же пакете, поэтому переименование
// или сдвиг битов подхватывается автоматически.

// WireConstants — снимок констант провода, зеркалимых клиентом.
type WireConstants struct {
	// Коды сообщений.
	MsgInput, MsgJoin, MsgSnapshot, MsgJoinAck, MsgSpawn, MsgDeath, MsgHit,
	MsgMatchState, MsgPickupState, MsgKillstreak, MsgWeaponState, MsgFlagState, MsgCapture uint8
	// Биты Input.Buttons (WASD + огонь), сдвиг и маска поля выбора оружия.
	BtnUp, BtnLeft, BtnDown, BtnRight, BtnFire uint8
	WeaponSelectShift                          uint8
	WeaponSelectMask                           uint8
	// Биты Input.Actions.
	ActDash uint8
	// Биты маски изменённых полей сущности в дельта-снапшоте.
	FieldKind, FieldX, FieldY, FieldVX, FieldVY, FieldHP, FieldAll uint8
	// Флаги MsgMatchState (режимы взаимоисключающи, но флаги независимы).
	MatchFlagTeamMode, MatchFlagHillMode, MatchFlagDomMode, MatchFlagCtfMode uint8
	// Шаг квантования координат/скоростей на проводе.
	CoordScale int
}

// ClientWire возвращает константы провода для генератора клиента (cmd/genclient).
func ClientWire() WireConstants {
	return WireConstants{
		MsgInput:       uint8(MsgInput),
		MsgJoin:        uint8(MsgJoin),
		MsgSnapshot:    uint8(MsgSnapshot),
		MsgJoinAck:     uint8(MsgJoinAck),
		MsgSpawn:       uint8(MsgSpawn),
		MsgDeath:       uint8(MsgDeath),
		MsgHit:         uint8(MsgHit),
		MsgMatchState:  uint8(MsgMatchState),
		MsgPickupState: uint8(MsgPickupState),
		MsgKillstreak:  uint8(MsgKillstreak),
		MsgWeaponState: uint8(MsgWeaponState),
		MsgFlagState:   uint8(MsgFlagState),
		MsgCapture:     uint8(MsgCapture),

		BtnUp:             BtnUp,
		BtnLeft:           BtnLeft,
		BtnDown:           BtnDown,
		BtnRight:          BtnRight,
		BtnFire:           BtnFire,
		WeaponSelectShift: weaponSelectShift,
		WeaponSelectMask:  weaponSelectMask,

		ActDash: ActDash,

		FieldKind: FieldKind,
		FieldX:    FieldX,
		FieldY:    FieldY,
		FieldVX:   FieldVX,
		FieldVY:   FieldVY,
		FieldHP:   FieldHP,
		FieldAll:  FieldAll,

		MatchFlagTeamMode: matchFlagTeamMode,
		MatchFlagHillMode: matchFlagHillMode,
		MatchFlagDomMode:  matchFlagDomMode,
		MatchFlagCtfMode:  matchFlagCtfMode,

		CoordScale: CoordScale,
	}
}
