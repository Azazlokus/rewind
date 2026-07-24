// Package protocol defines the messages exchanged between client and server.
//
// It is pure data: it must never import a game package, so that the codec can be
// fuzzed, benchmarked and versioned on its own.
//
// Wire format v1 (little-endian, first byte is the message type):
//
//	client -> server
//	  MsgInput  0x01  [1B type][4B seq][1B buttons][2B aim]
//	  MsgJoin   0x02  [1B type][1B nameLen][name UTF-8, max 16B]
//	server -> client
//	  MsgSnapshot 0x10 [1B][4B tick][4B lastProcessedSeq][1B count]
//	                   count x [2B id][1B kind][2B x][2B y][2B vx][2B vy][1B hp]
//	  MsgJoinAck  0x11 [1B][2B yourID][4B tick]
//	  MsgSpawn    0x12, MsgDeath 0x13, MsgHit 0x14
//
// Iteration 1 carries these same structures as JSON while the game loop is being
// built; iteration 3 replaces the codec with the binary layout above. Everything
// outside this package is written against the types, not the encoding.
package protocol

import (
	"errors"
	"math"
)

// MsgType is the first byte of every message.
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

// String reports the message type name, for logs and test failures.
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

// Button bits carried by Input.Buttons: bits 0..3 are WASD, bit 4 is fire.
const (
	BtnUp uint8 = 1 << iota
	BtnLeft
	BtnDown
	BtnRight
	BtnFire
)

const (
	// MaxNameLen is the maximum player name length in bytes.
	MaxNameLen = 16
	// MaxEntities is the maximum number of entities in one snapshot; the count
	// is a single byte on the wire.
	MaxEntities = 255
	// MapSize is the side of the square map in world units.
	MapSize = 4096
	// CoordScale is the quantisation step of positions on the wire: 1/16 unit.
	CoordScale = 16
	// MaxSpeed bounds the velocity range that fits in the quantised velocity
	// fields of a snapshot.
	MaxSpeed = 2048
)

// Codec errors. The decoder never panics; malformed input always surfaces here.
var (
	ErrEmptyMessage  = errors.New("protocol: empty message")
	ErrShortMessage  = errors.New("protocol: message truncated")
	ErrUnknownType   = errors.New("protocol: unknown message type")
	ErrNameTooLong   = errors.New("protocol: name too long")
	ErrTooManyEntity = errors.New("protocol: too many entities")
	ErrMalformed     = errors.New("protocol: malformed message")
)

// EntityKind tags an entity in a snapshot.
type EntityKind uint8

const (
	KindPlayer     EntityKind = 1
	KindProjectile EntityKind = 2
)

// Input is one client command, produced at 60 Hz.
type Input struct {
	Seq     uint32 `json:"s"`
	Buttons uint8  `json:"b"`
	Aim     uint16 `json:"a"`
}

// Join is the first message a client sends.
type Join struct {
	Name string `json:"n"`
}

// Entity is one entity as it appears in a snapshot.
type Entity struct {
	ID   uint16     `json:"i"`
	Kind EntityKind `json:"k"`
	X    float32    `json:"x"`
	Y    float32    `json:"y"`
	VX   float32    `json:"vx"`
	VY   float32    `json:"vy"`
	HP   uint8      `json:"hp"`
}

// Snapshot is the server's view of the world at one tick.
type Snapshot struct {
	Tick uint32 `json:"t"`
	// LastProcessedSeq is per receiver: it is the sequence number of the last
	// input of that client the server has simulated. Client reconciliation in
	// iteration 4 is built on it.
	LastProcessedSeq uint32   `json:"ls"`
	Entities         []Entity `json:"e"`
}

// JoinAck answers a Join and tells the client which entity is its own.
type JoinAck struct {
	YourID uint16 `json:"i"`
	Tick   uint32 `json:"t"`
}

// AimRadians converts the quantised aim angle to radians in [0, 2π).
func (in Input) AimRadians() float32 {
	return float32(float64(in.Aim) * (2 * math.Pi / 65536))
}

// AimFromRadians quantises an angle to the wire representation.
func AimFromRadians(rad float64) uint16 {
	const turn = 2 * math.Pi
	rad = math.Mod(rad, turn)
	if rad < 0 {
		rad += turn
	}
	return uint16(math.Round(rad*(65536/turn))) & 0xffff
}

// Pressed reports whether all bits of mask are held down.
func (in Input) Pressed(mask uint8) bool { return in.Buttons&mask == mask }
