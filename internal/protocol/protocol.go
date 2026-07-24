// Пакет protocol описывает сообщения, которыми обмениваются клиент и сервер.
//
// Это чистые данные: он никогда не импортирует игровой пакет, чтобы кодек можно
// было фаззить, бенчмаркать и версионировать отдельно.
//
// Формат провода v1 (little-endian, первый байт — тип сообщения):
//
//	клиент -> сервер
//	  MsgInput  0x01  [1B type][4B seq][1B buttons][2B aim]
//	  MsgJoin   0x02  [1B type][1B nameLen][name UTF-8, max 16B]
//	сервер -> клиент
//	  MsgSnapshot 0x10 [1B][4B tick][4B lastProcessedSeq][1B count]
//	                   count x [2B id][1B kind][2B x][2B y][2B vx][2B vy][1B hp]
//	  MsgJoinAck  0x11 [1B][2B yourID][4B tick]
//	  MsgSpawn    0x12, MsgDeath 0x13, MsgHit 0x14
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
	default:
		return "Unknown"
	}
}

// Биты кнопок в Input.Buttons: биты 0..3 = WASD, бит 4 = fire.
const (
	BtnUp uint8 = 1 << iota
	BtnLeft
	BtnDown
	BtnRight
	BtnFire
)

const (
	// MaxNameLen — максимальная длина имени игрока в байтах.
	MaxNameLen = 16
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
	ErrTooManyEntity = errors.New("protocol: too many entities")
	ErrMalformed     = errors.New("protocol: malformed message")
)

// EntityKind помечает сущность в снапшоте.
type EntityKind uint8

const (
	KindPlayer     EntityKind = 1
	KindProjectile EntityKind = 2
)

// Input — одна клиентская команда, производится на 60 Гц.
type Input struct {
	Seq     uint32 `json:"s"`
	Buttons uint8  `json:"b"`
	Aim     uint16 `json:"a"`
}

// Join — первое сообщение, которое шлёт клиент.
type Join struct {
	Name string `json:"n"`
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
type Snapshot struct {
	Tick uint32 `json:"t"`
	// LastProcessedSeq — своё для каждого получателя: номер последнего ввода
	// этого клиента, который сервер уже просимулировал. На нём строится
	// клиентская реконсиляция в итерации 4.
	LastProcessedSeq uint32   `json:"ls"`
	Entities         []Entity `json:"e"`
}

// JoinAck отвечает на Join и сообщает клиенту, какая сущность — его.
type JoinAck struct {
	YourID uint16 `json:"i"`
	Tick   uint32 `json:"t"`
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
