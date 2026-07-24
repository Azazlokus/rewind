package game

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	rand "math/rand/v2"
	"slices"

	"arena/internal/protocol"
)

// ErrWorldFull is returned when no entity id is available.
var ErrWorldFull = errors.New("game: world is full")

// PlayerID identifies a player inside one world. It doubles as the entity id on
// the wire, so it is a uint16.
type PlayerID uint16

// Player is one connected participant.
type Player struct {
	ID   PlayerID
	Name string
	MoveState
	HP uint8

	// LastProcessedSeq is the sequence number of the newest input the
	// simulation has applied. The client uses it to discard acknowledged
	// inputs during reconciliation (iteration 4).
	LastProcessedSeq uint32

	// input is the most recent command received. Clients send at 60 Hz and the
	// server ticks at 30 Hz, so iteration 1 applies the newest command and
	// drops the rest; iteration 4 replaces this with a proper input queue.
	input protocol.Input
}

// World holds the authoritative game state of one room.
//
// It is owned by a single goroutine, the room's game loop, and carries no locks
// on purpose: every mutation happens between two ticks, in one place. The world
// runs perfectly well with no network at all, which is what the headless tests
// and the replay tool rely on.
type World struct {
	// Tick is the number of simulated steps since the world was created.
	Tick uint32

	players map[PlayerID]*Player
	// order holds the ids of players in ascending order. All iteration goes
	// through it: ranging over a Go map is randomised and would make the
	// simulation non-deterministic.
	order  []PlayerID
	rng    *rand.Rand
	nextID PlayerID
}

// NewWorld creates an empty world. Two worlds created with the same seed and fed
// the same inputs produce byte-identical states; TestWorldDeterminism enforces
// it, and replays depend on it.
func NewWorld(seed int64) *World {
	return &World{
		players: make(map[PlayerID]*Player),
		rng:     rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15)),
		nextID:  1,
	}
}

// Len reports the number of players in the world.
func (w *World) Len() int { return len(w.players) }

// Player returns the player with the given id, or nil.
func (w *World) Player(id PlayerID) *Player { return w.players[id] }

// AddPlayer places a new player at a seeded random spawn point.
func (w *World) AddPlayer(name string) (*Player, error) {
	id, err := w.allocID()
	if err != nil {
		return nil, err
	}
	p := &Player{
		ID:        id,
		Name:      name,
		MoveState: w.spawnPoint(),
		HP:        100,
	}
	w.players[id] = p
	i, _ := slices.BinarySearch(w.order, id)
	w.order = slices.Insert(w.order, i, id)
	return p, nil
}

// RemovePlayer drops a player. Removing an unknown id is a no-op.
func (w *World) RemovePlayer(id PlayerID) {
	if _, ok := w.players[id]; !ok {
		return
	}
	delete(w.players, id)
	if i, ok := slices.BinarySearch(w.order, id); ok {
		w.order = slices.Delete(w.order, i, i+1)
	}
}

// SetInput records the newest command of a player.
func (w *World) SetInput(id PlayerID, in protocol.Input) {
	p := w.players[id]
	if p == nil {
		return
	}
	p.input = in
}

// Step advances the whole world by dt seconds.
func (w *World) Step(dt float32) {
	for _, id := range w.order {
		p := w.players[id]
		Step(&p.MoveState, p.input, dt)
		// Sequence numbers only move forward: a client that replays or forges
		// an old number must not walk the acknowledgement backwards.
		if p.input.Seq > p.LastProcessedSeq {
			p.LastProcessedSeq = p.input.Seq
		}
	}
	w.Tick++
}

// Each calls f for every player in deterministic id order.
func (w *World) Each(f func(*Player)) {
	for _, id := range w.order {
		f(w.players[id])
	}
}

// AppendEntities appends every entity of the world to dst in id order and
// returns the extended slice. Callers pass a reused slice to stay allocation
// free on the hot path.
func (w *World) AppendEntities(dst []protocol.Entity) []protocol.Entity {
	for _, id := range w.order {
		p := w.players[id]
		dst = append(dst, protocol.Entity{
			ID:   uint16(p.ID),
			Kind: protocol.KindPlayer,
			X:    p.X,
			Y:    p.Y,
			VX:   p.VX,
			VY:   p.VY,
			HP:   p.HP,
		})
	}
	return dst
}

// Checksum is a hash of the full simulation state. It is the equality test used
// by the determinism test and by replay verification: identical checksums mean
// identical worlds, down to the last float bit.
func (w *World) Checksum() uint64 {
	h := fnv.New64a()
	var buf [8]byte
	writeU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(buf[:4], v)
		_, _ = h.Write(buf[:4])
	}
	writeF32 := func(v float32) { writeU32(math.Float32bits(v)) }

	writeU32(w.Tick)
	writeU32(uint32(len(w.order)))
	for _, id := range w.order {
		p := w.players[id]
		writeU32(uint32(p.ID))
		writeU32(p.LastProcessedSeq)
		writeF32(p.X)
		writeF32(p.Y)
		writeF32(p.VX)
		writeF32(p.VY)
		buf[0] = p.HP
		_, _ = h.Write(buf[:1])
		_, _ = h.Write([]byte(p.Name))
	}
	return h.Sum64()
}

// allocID finds a free entity id, scanning forward from the last one handed out
// so that ids are not reused immediately after a player leaves.
func (w *World) allocID() (PlayerID, error) {
	for range math.MaxUint16 {
		id := w.nextID
		w.nextID++
		if w.nextID == 0 {
			w.nextID = 1
		}
		if _, taken := w.players[id]; !taken && id != 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("%w: %d players", ErrWorldFull, len(w.players))
}

// spawnPoint picks a position from the world's own generator. The simulation
// must never touch the global rand or the wall clock.
func (w *World) spawnPoint() MoveState {
	const margin = 128
	span := MapSize - 2*margin
	return MoveState{
		X: margin + w.rng.Float32()*span,
		Y: margin + w.rng.Float32()*span,
	}
}
