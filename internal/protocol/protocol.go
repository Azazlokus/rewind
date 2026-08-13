// Пакет protocol описывает сообщения, которыми обмениваются клиент и сервер.
//
// Это чистые данные: он никогда не импортирует игровой пакет, чтобы кодек можно
// было фаззить, бенчмаркать и версионировать отдельно.
//
// Формат провода v1 (little-endian, первый байт — тип сообщения):
//
//	клиент -> сервер
//	  MsgInput  0x01  [1B type][4B seq][1B buttons][2B aim][4B viewTick][4B ackTick]
//	                   [1B actions?] — actions опционален (итер. 27): бит0 = рывок (ActDash).
//	                   Старый ввод без этого байта декодируется с actions=0 (нет абилок).
//	  MsgJoin   0x02  [1B type][1B nameLen][name UTF-8, max 16B]
//	                   [2B tokenLen][token UTF-8, max 512B][1B spectator?]
//	                   token — токен-сессия (итер. 14B); tokenLen 0 — аноним/гость.
//	                   spectator (итер. 22) — опциональный байт: 1 = наблюдатель без
//	                   спавна; отсутствует/0 — обычный игрок.
//	сервер -> клиент
//	  MsgSnapshot 0x10 [1B][4B tick][4B baseTick][4B lastProcessedSeq][1B changed]
//	                   changed x [2B id][1B kind][2B x][2B y][2B vx][2B vy][1B hp]
//	                   [1B removed] removed x [2B id]
//	                   baseTick == 0 — полный снапшот (changed = весь набор, removed
//	                   пуст); иначе дельта против снапшота с меткой baseTick: changed —
//	                   новые/изменённые сущности, removed — id ушедших.
//	  MsgJoinAck  0x11 [1B][2B yourID][4B tick]
//	  MsgSpawn    0x12 [1B][2B id][2B x][2B y]                       (reliable)
//	  MsgDeath    0x13 [1B][2B victimID][2B killerID]                (reliable)
//	  MsgHit      0x14 [1B][2B attackerID][2B victimID][1B dmg][1B victimHP] (reliable)
//	  MsgMatchState 0x15 [1B][1B phase][4B remaining][2B winner][1B flags][1B count]
//	                   count x [2B id][2B kills][2B deaths][1B team][2B hillScore][1B nameLen][name] (reliable)
//	                   flags bit0 = teamMode (итер. 23): winner — id команды, а не игрока;
//	                   flags bit1 = hillMode (итер. 29): счёт/победитель по очкам холма.
//	  MsgPickupState 0x16 [1B][1B count] count x [1B spot][1B kind]            (reliable)
//	                   активные пикапы: spot — индекс фиксированной точки (клиент
//	                   зеркалит раскладку), kind — тип (1 аптечка / 2 ускорение /
//	                   3 веер). Полный набор активных точек; точка не в списке — пуста.
//	  MsgKillstreak 0x17 [1B][2B id][2B streak]                               (reliable)
//	                   игрок id достиг вехи серии убийств длиной streak (итер. 20).
//	  MsgWeaponState 0x18 [1B][1B count] count x [2B id][1B weapon]           (reliable)
//	                   текущее оружие каждого игрока (1 пистолет / 2 дробовик /
//	                   3 снайперка / 4 ракета). Полный набор; событийно при смене/входе.
//
// Итерация 1 переносит эти же структуры как JSON, пока строится game loop;
// итерация 3 заменит кодек на бинарную раскладку выше. Всё вне этого пакета
// написано против типов, а не против кодировки.
package protocol

import (
	"errors"
	"math"
)

// MsgType — первый байт каждого сообщения.
type MsgType uint8

const (
	MsgInput    MsgType = 0x01
	MsgJoin     MsgType = 0x02
	MsgSnapshot MsgType = 0x10
	MsgJoinAck  MsgType = 0x11
	MsgSpawn    MsgType = 0x12
	MsgDeath    MsgType = 0x13
	MsgHit      MsgType = 0x14
	// MsgMatchState — reliable-событие: фаза матча, остаток времени и табло (итер. 14).
	MsgMatchState MsgType = 0x15
	// MsgPickupState — reliable-событие: какие точки пикапов сейчас заняты и чем
	// (итерация 19). Полное состояние (не дельта), шлётся событийно при изменении.
	MsgPickupState MsgType = 0x16
	// MsgKillstreak — reliable-событие: игрок достиг вехи серии убийств (итер. 20).
	MsgKillstreak MsgType = 0x17
	// MsgWeaponState — reliable-событие: текущее оружие каждого игрока (итер. 26).
	// Полный набор, шлётся событийно при смене оружия/входе (как MsgPickupState).
	MsgWeaponState MsgType = 0x18
)

// String возвращает имя типа сообщения — для логов и падений тестов.
func (t MsgType) String() string {
	switch t {
	case MsgInput:
		return "Input"
	case MsgJoin:
		return "Join"
	case MsgSnapshot:
		return "Snapshot"
	case MsgJoinAck:
		return "JoinAck"
	case MsgSpawn:
		return "Spawn"
	case MsgDeath:
		return "Death"
	case MsgHit:
		return "Hit"
	case MsgMatchState:
		return "MatchState"
	case MsgPickupState:
		return "PickupState"
	case MsgKillstreak:
		return "Killstreak"
	case MsgWeaponState:
		return "WeaponState"
	default:
		return "Unknown"
	}
}

// Биты кнопок в Input.Buttons: биты 0..3 = WASD, бит 4 = fire. Биты 5..7 несут
// выбор оружия (итер. 26): 3-битное поле 0..7, где 0 = «не менять», 1..4 = выбрать
// оружие (см. WeaponSelect). Так переключение оружия не меняет формат провода ввода —
// это по-прежнему один байт Buttons.
const (
	BtnUp uint8 = 1 << iota
	BtnLeft
	BtnDown
	BtnRight
	BtnFire
)

// weaponSelectShift/weaponSelectMask выделяют поле выбора оружия в Buttons (биты 5..7).
const (
	weaponSelectShift = 5
	weaponSelectMask  = 0x07
)

// Биты Input.Actions — действия/абилки, не влезшие в занятый Buttons (итер. 27).
// Отдельный опциональный байт на проводе; биты 1..7 зарезервированы под будущие абилки.
const (
	// ActDash — запрос рывка (короткий рывок-ускорение в сторону движения).
	ActDash uint8 = 1 << 0
)

const (
	// MaxNameLen — максимальная длина имени игрока в байтах.
	MaxNameLen = 16
	// MaxTokenLen — максимальная длина токен-сессии в Join в байтах (итер. 14B).
	// Токен — base64url(json).base64url(hmac) (см. internal/account); с запасом на
	// имя и будущие claims. Ограничивает аллокацию декодера на кривом/враждебном вводе.
	MaxTokenLen = 512
	// MaxEntities — максимум сущностей в одном снапшоте; count на проводе — один
	// байт.
	MaxEntities = 255
	// MapSize — сторона квадратной карты в мировых юнитах.
	MapSize = 4096
	// CoordScale — шаг квантования позиций на проводе: 1/16 юнита.
	CoordScale = 16
	// MaxSpeed ограничивает диапазон скорости, влезающий в квантованные поля
	// скорости снапшота.
	MaxSpeed = 2048
)

// Ошибки кодека. Декодер никогда не паникует; кривой ввод всегда всплывает здесь.
var (
	ErrEmptyMessage  = errors.New("protocol: empty message")
	ErrShortMessage  = errors.New("protocol: message truncated")
	ErrUnknownType   = errors.New("protocol: unknown message type")
	ErrNameTooLong   = errors.New("protocol: name too long")
	ErrTokenTooLong  = errors.New("protocol: token too long")
	ErrTooManyEntity = errors.New("protocol: too many entities")
	ErrMalformed     = errors.New("protocol: malformed message")
)

// EntityKind помечает сущность в снапшоте.
type EntityKind uint8

const (
	KindPlayer     EntityKind = 1
	KindProjectile EntityKind = 2
)

// Биты маски изменённых полей сущности в дельта-снапшоте (итерация 9). В дельте
// (BaseTick != 0) каждая изменённая запись несёт [2B id][1B маска][только
// присутствующие поля] в этом фиксированном порядке; получатель накладывает их на
// свою базу с меткой BaseTick. Полный снапшот (BaseTick == 0) маску не использует —
// там всегда все поля 12-байтной записью. Значения зеркалит web/game.js.
const (
	FieldKind uint8 = 1 << iota // 1B
	FieldX                      // 2B
	FieldY                      // 2B
	FieldVX                     // 2B
	FieldVY                     // 2B
	FieldHP                     // 1B
)

// FieldAll — все определённые биты маски: маска новой (отсутствующей в базе)
// сущности, все поля которой надо прислать. Неизвестные биты декодер отбрасывает.
const FieldAll = FieldKind | FieldX | FieldY | FieldVX | FieldVY | FieldHP

// Флаги MsgMatchState. Байт флагов идёт после winner.
const (
	matchFlagTeamMode uint8 = 1 << 0 // командный режим (итер. 23): winner — id команды
	matchFlagHillMode uint8 = 1 << 1 // King of the Hill (итер. 29): счёт/победитель по холму
)

// Input — одна клиентская команда, производится на 60 Гц.
type Input struct {
	Seq     uint32 `json:"s"`
	Buttons uint8  `json:"b"`
	Aim     uint16 `json:"a"`
	// ViewTick — серверный тик, к которому клиент интерполировал в момент этого
	// ввода (что игрок реально видел). Сервер использует его для lag compensation:
	// перематывает цели к этому тику, зажимая в окно перемотки. 0 — «не знаю»
	// (клиент ещё не получал снапшотов); тогда сервер бьёт по настоящему.
	ViewTick uint32 `json:"vt"`
	// AckTick — метка последнего снапшота, который клиент полностью получил и
	// реконструировал (итерация 6B). Сервер кодирует следующий снапшот дельтой
	// против него; 0 — «ещё ничего не подтверждено» (тогда сервер шлёт полный).
	AckTick uint32 `json:"at"`
	// Actions — биты действий/абилок (итер. 27), напр. ActDash. Отдельно от Buttons,
	// т.к. в Buttons свободных бит нет (WASD+fire+выбор оружия занимают все 8).
	Actions uint8 `json:"ac"`
}

// Join — первое сообщение, которое шлёт клиент. Token (итер. 14B) — подписанный
// токен-сессия из бэкенда (register/login/guest): по нему шлюз привязывает сессию к
// аккаунту. Пусто — аноним: сервер заводит гостя с указанным Name.
type Join struct {
	Name  string `json:"n"`
	Token string `json:"t"`
	// Spectator (итер. 22) — клиент хочет наблюдать без спавна: не создаётся Player,
	// не участвует в бою, только получает снапшоты и события. Опционально на проводе
	// (байт после токена); отсутствие = обычный игрок.
	Spectator bool `json:"sp"`
}

// Entity — одна сущность, как она выглядит в снапшоте.
type Entity struct {
	ID   uint16     `json:"i"`
	Kind EntityKind `json:"k"`
	X    float32    `json:"x"`
	Y    float32    `json:"y"`
	VX   float32    `json:"vx"`
	VY   float32    `json:"vy"`
	HP   uint8      `json:"hp"`
}

// Snapshot — взгляд сервера на мир на одном тике.
//
// С итерации 6B снапшот может быть дельтой. BaseTick == 0 — полный снапшот:
// Entities несёт весь набор, Removed пуст. BaseTick != 0 — дельта против снапшота
// с меткой BaseTick: Entities — новые/изменённые сущности, Removed — id тех, кто
// был в базе и пропал. Реконструкция полного набора из базы и дельты — на стороне
// получателя (клиент/бот), кодек лишь переносит куски.
type Snapshot struct {
	Tick uint32 `json:"t"`
	// BaseTick — метка снапшота-базы для дельты; 0 означает полный снапшот.
	BaseTick uint32 `json:"bt"`
	// LastProcessedSeq — своё для каждого получателя: номер последнего ввода
	// этого клиента, который сервер уже просимулировал. На нём строится
	// клиентская реконсиляция в итерации 4.
	LastProcessedSeq uint32   `json:"ls"`
	Entities         []Entity `json:"e"`
	// Masks — параллельная Entities маска изменённых полей (итерация 9), значима
	// только для дельты (BaseTick != 0): Masks[i] говорит, какие поля Entities[i]
	// присутствуют на проводе и авторитетны; остальные поля Entities[i] несут нули и
	// должны браться из базы. Для полного снапшота nil (все поля всегда присутствуют).
	Masks []uint8 `json:"m"`
	// Removed — id сущностей, ушедших с прошлого снапшота (только для дельты).
	Removed []uint16 `json:"r"`
}

// JoinAck отвечает на Join и сообщает клиенту, какая сущность — его.
type JoinAck struct {
	YourID uint16 `json:"i"`
	Tick   uint32 `json:"t"`
}

// Spawn — reliable-событие: сущность id (пере)родилась в точке (X, Y). Владельцу
// даёт сбросить предсказание на авторитетную точку спавна, остальным — сразу
// поставить сущность, не дожидаясь снапшота.
type Spawn struct {
	ID uint16  `json:"i"`
	X  float32 `json:"x"`
	Y  float32 `json:"y"`
}

// Death — reliable-событие: Victim убит Killer'ом (Killer == Victim при суициде о
// границу или урон окружения — пока не используется). Идёт всем клиентам.
type Death struct {
	Victim uint16 `json:"v"`
	Killer uint16 `json:"k"`
}

// Hit — reliable-событие: Attacker попал по Victim на Damage урона, HP цели стал
// VictimHP. Идёт участникам (атакующему и жертве) для фидбэка.
type Hit struct {
	Attacker uint16 `json:"a"`
	Victim   uint16 `json:"v"`
	Damage   uint8  `json:"d"`
	VictimHP uint8  `json:"hp"`
}

// MatchScore — строка табло: игрок, его счёт за текущий матч, команда (итер. 23) и
// очки холма (итер. 29).
type MatchScore struct {
	ID        uint16 `json:"i"`
	Name      string `json:"n"`
	Kills     uint16 `json:"k"`
	Deaths    uint16 `json:"d"`
	Team      uint8  `json:"tm"`
	HillScore uint16 `json:"h"`
}

// MatchState — reliable-событие состояния матча (итерация 14). Phase: 0 — идёт бой,
// 1 — антракт. Remaining — тиков до смены фазы. Winner — id победителя (валиден в
// антракте, иначе 0). Scores — табло по убыванию убийств. TeamMode (итер. 23) —
// командный режим: Winner несёт id ПОБЕДИВШЕЙ КОМАНДЫ (0/1), а не игрока; каждая
// строка табло несёт команду игрока.
type MatchState struct {
	Phase     uint8        `json:"p"`
	Remaining uint32       `json:"rem"`
	Winner    uint16       `json:"w"`
	TeamMode  bool         `json:"tmm"`
	HillMode  bool         `json:"hm"`
	Scores    []MatchScore `json:"s"`
}

// Pickup — активный пикап в снимке состояния пикапов (итерация 19). Spot — индекс
// фиксированной точки появления (клиент зеркалит их координаты, как walls); Kind —
// тип пикапа (1 аптечка / 2 ускорение стрельбы / 3 веер).
type Pickup struct {
	Spot uint8 `json:"s"`
	Kind uint8 `json:"k"`
}

// PickupState — reliable-снимок пикапов (итерация 19): полный список сейчас
// активных точек. Точка, которой нет в Active, считается пустой. Шлётся событийно
// при изменении (спавн/подбор), как MatchState.
type PickupState struct {
	Active []Pickup `json:"a"`
}

// Killstreak — reliable-событие: игрок ID достиг вехи серии убийств длиной Streak
// (итерация 20). Идёт всем клиентам для объявления/фида и щит-визуала.
type Killstreak struct {
	ID     uint16 `json:"i"`
	Streak uint16 `json:"s"`
}

// WeaponInfo — оружие одного игрока в снимке MsgWeaponState (итер. 26). Weapon —
// тип оружия (1 пистолет / 2 дробовик / 3 снайперка / 4 ракета).
type WeaponInfo struct {
	ID     uint16 `json:"i"`
	Weapon uint8  `json:"w"`
}

// WeaponState — reliable-снимок оружия всех игроков (итер. 26): полный набор,
// шлётся событийно при смене оружия/входе (как PickupState). Клиент строит карту
// id→оружие для HUD и подписи над бойцами.
type WeaponState struct {
	Weapons []WeaponInfo `json:"w"`
}

// AimRadians переводит квантованный угол прицела в радианы в [0, 2π).
func (in Input) AimRadians() float32 {
	return float32(float64(in.Aim) * (2 * math.Pi / 65536))
}

// AimFromRadians квантует угол в представление на проводе.
func AimFromRadians(rad float64) uint16 {
	const turn = 2 * math.Pi
	rad = math.Mod(rad, turn)
	if rad < 0 {
		rad += turn
	}
	return uint16(math.Round(rad*(65536/turn))) & 0xffff
}

// Pressed сообщает, зажаты ли все биты маски mask.
func (in Input) Pressed(mask uint8) bool { return in.Buttons&mask == mask }

// WeaponSelect возвращает запрошенный выбор оружия из старших битов Buttons (итер. 26):
// 0 — «не менять», 1..4 — выбрать оружие. Значения вне диапазона оружия сервер
// игнорирует. Движение/огонь (биты 0..4) не затрагиваются.
func (in Input) WeaponSelect() uint8 { return (in.Buttons >> weaponSelectShift) & weaponSelectMask }

// Action сообщает, запрошено ли действие mask в Input.Actions (итер. 27).
func (in Input) Action(mask uint8) bool { return in.Actions&mask == mask }
