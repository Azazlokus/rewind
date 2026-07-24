package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"arena/internal/protocol"
	"arena/internal/transport"
)

var (
	// ErrRoomFull is returned by Join when the room is at capacity.
	ErrRoomFull = errors.New("game: room is full")
	// ErrRoomClosed is returned by Join once the room's loop has stopped.
	ErrRoomClosed = errors.New("game: room is closed")
)

// Config tunes a room. The zero value is usable; every field has a default.
type Config struct {
	// TickRate is the simulation frequency in Hz. Default 30.
	TickRate int
	// SnapshotRate is how often snapshots go out, in Hz. It must divide
	// TickRate. Default: equal to TickRate, which is what iteration 1 wants;
	// iteration 2 lowers it to 20 and lets client interpolation hide the gap.
	SnapshotRate int
	// MaxPlayers caps the room. Default 64.
	MaxPlayers int
	// InboxSize is the depth of the room's event queue. Default 1024.
	InboxSize int
	// SessionQueue is the depth of one client's outbound queue. It is small on
	// purpose: queued snapshots are stale snapshots. Default 8.
	SessionQueue int
	// MaxBacklog is how many consecutive snapshots a client may miss before it
	// is disconnected. Default 30, i.e. about a second at 30 Hz.
	MaxBacklog int
	// Seed feeds the world's generator. Equal seeds and equal inputs give
	// equal worlds.
	Seed int64
	// Clock defaults to RealClock. Tests pass a ManualClock.
	Clock Clock
	// Metrics defaults to NopRecorder.
	Metrics Recorder
	// Logger defaults to a discarding logger.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.TickRate <= 0 {
		c.TickRate = 30
	}
	if c.SnapshotRate <= 0 || c.SnapshotRate > c.TickRate {
		c.SnapshotRate = c.TickRate
	}
	if c.MaxPlayers <= 0 {
		c.MaxPlayers = 64
	}
	if c.InboxSize <= 0 {
		c.InboxSize = 1024
	}
	if c.SessionQueue <= 0 {
		c.SessionQueue = 8
	}
	if c.MaxBacklog <= 0 {
		c.MaxBacklog = 30
	}
	if c.Clock == nil {
		c.Clock = RealClock{}
	}
	if c.Metrics == nil {
		c.Metrics = NopRecorder{}
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// TickInterval is the wall time between two simulation steps.
func (c Config) TickInterval() time.Duration {
	return time.Second / time.Duration(c.TickRate)
}

// snapshotEvery is how many ticks pass between two snapshots.
func (c Config) snapshotEvery() uint32 {
	every := c.TickRate / c.SnapshotRate
	if every < 1 {
		return 1
	}
	return uint32(every)
}

// Room is one game instance: a world, a fixed-rate loop and a set of sessions.
//
// Everything that touches the world happens on the loop goroutine. The outside
// world talks to a room exclusively through inbox events, which is why the game
// state carries no mutexes. Any code reaching into World from another goroutine
// is a bug, not an optimisation.
type Room struct {
	id  string
	cfg Config
	log *slog.Logger

	inbox chan event
	done  chan struct{}

	// Owned by the loop goroutine.
	world    *World
	sessions map[PlayerID]*Session
	entities []protocol.Entity // reused snapshot scratch
	kicked   []PlayerID        // reused per-tick list of sessions to drop

	// Observable from other goroutines.
	players    atomic.Int32
	inputDrops atomic.Uint64
	started    atomic.Bool
}

// NewRoom creates a room. It does not start the loop; call Run.
func NewRoom(id string, cfg Config) *Room {
	cfg = cfg.withDefaults()
	return &Room{
		id:       id,
		cfg:      cfg,
		log:      cfg.Logger.With("room", id),
		inbox:    make(chan event, cfg.InboxSize),
		done:     make(chan struct{}),
		world:    NewWorld(cfg.Seed),
		sessions: make(map[PlayerID]*Session),
		entities: make([]protocol.Entity, 0, cfg.MaxPlayers),
	}
}

// ID is the room's identifier.
func (r *Room) ID() string { return r.id }

// Config returns the room's effective configuration, with defaults applied. It
// is read-only state fixed at construction, so it is safe to read from any
// goroutine; tests use it to reach the injected Clock.
func (r *Room) Config() Config { return r.cfg }

// Players is the current player count. It is a snapshot value for the hub and
// for metrics, never a basis for game logic.
func (r *Room) Players() int { return int(r.players.Load()) }

// Done is closed when the loop has stopped and every session has been released.
func (r *Room) Done() <-chan struct{} { return r.done }

// DroppedInputs counts client commands discarded because the inbox was full.
func (r *Room) DroppedInputs() uint64 { return r.inputDrops.Load() }

// Run drives the room until ctx is cancelled. It owns the world for its whole
// lifetime, and returns only after the current tick has finished and all
// sessions have been closed.
func (r *Room) Run(ctx context.Context) {
	if !r.started.Swap(true) {
		defer close(r.done)
	} else {
		panic("game: room " + r.id + " started twice")
	}

	interval := r.cfg.TickInterval()
	dt := float32(interval.Seconds())
	ticker := r.cfg.Clock.NewTicker(interval)
	defer ticker.Stop()

	r.log.Info("room started", "tick_rate", r.cfg.TickRate, "snapshot_rate", r.cfg.SnapshotRate)
	for {
		select {
		case <-ctx.Done():
			// The tick in progress, if any, has already returned: shutdown
			// never interrupts a half-simulated world.
			r.shutdown()
			r.log.Info("room stopped", "tick", r.world.Tick)
			return
		case <-ticker.C():
			r.tick(dt)
		}
	}
}

// Join registers a client and returns its session. The caller owns the returned
// session and must call Run on it.
func (r *Room) Join(ctx context.Context, conn transport.Conn, name string) (*Session, error) {
	req := &joinReq{conn: conn, name: name, reply: make(chan joinResult, 1)}
	if err := r.post(ctx, event{kind: evJoin, join: req}); err != nil {
		return nil, err
	}
	select {
	case res := <-req.reply:
		if res.err != nil {
			return nil, res.err
		}
		return res.sess, nil
	case <-r.done:
		return nil, ErrRoomClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("game: join %s: %w", r.id, ctx.Err())
	}
}

// State returns a copy of the current world state. It is answered by the loop
// goroutine, so it is both race free and a synchronisation point: when it
// returns, every event posted before it has been applied.
func (r *Room) State(ctx context.Context) (protocol.Snapshot, error) {
	reply := make(chan protocol.Snapshot, 1)
	if err := r.post(ctx, event{kind: evState, state: reply}); err != nil {
		return protocol.Snapshot{}, err
	}
	select {
	case s := <-reply:
		return s, nil
	case <-r.done:
		return protocol.Snapshot{}, ErrRoomClosed
	case <-ctx.Done():
		return protocol.Snapshot{}, fmt.Errorf("game: state %s: %w", r.id, ctx.Err())
	}
}

// leave asks the room to release a player. It is idempotent.
func (r *Room) leave(ctx context.Context, id PlayerID) {
	// A failed post means the room is already gone or shutting down, and it
	// releases its sessions itself in that case.
	_ = r.post(ctx, event{kind: evLeave, id: id})
}

// input forwards a client command. Commands are lossy by nature: if the room is
// so far behind that its inbox is full, dropping this command is better than
// blocking a read pump, since a newer one follows in about 16 ms.
func (r *Room) input(_ context.Context, id PlayerID, in protocol.Input) {
	select {
	case r.inbox <- event{kind: evInput, id: id, input: in}:
	default:
		r.inputDrops.Add(1)
	}
}

// post queues an event, giving up if the caller or the room goes away.
func (r *Room) post(ctx context.Context, ev event) error {
	select {
	case r.inbox <- ev:
		return nil
	case <-r.done:
		return ErrRoomClosed
	case <-ctx.Done():
		return fmt.Errorf("game: post to %s: %w", r.id, ctx.Err())
	}
}

// tick is one simulation step: apply everything that arrived, advance the
// world, then talk to the clients.
func (r *Room) tick(dt float32) {
	start := r.cfg.Clock.Now()

	r.drainInbox()
	r.world.Step(dt)
	if r.world.Tick%r.cfg.snapshotEvery() == 0 {
		r.broadcast()
	}
	r.dropLaggards()

	r.cfg.Metrics.TickDuration(r.cfg.Clock.Now().Sub(start))
	r.cfg.Metrics.InboxDepth(len(r.inbox))
}

// drainInbox applies every queued event. The bound guarantees the loop reaches
// the simulation step even while clients keep posting.
func (r *Room) drainInbox() {
	for range cap(r.inbox) {
		select {
		case ev := <-r.inbox:
			r.handle(ev)
		default:
			return
		}
	}
}

func (r *Room) handle(ev event) {
	switch ev.kind {
	case evJoin:
		r.handleJoin(ev.join)
	case evLeave:
		r.removeSession(ev.id, "left")
	case evInput:
		r.world.SetInput(ev.id, ev.input)
	case evState:
		snap := protocol.Snapshot{Tick: r.world.Tick}
		snap.Entities = r.world.AppendEntities(nil)
		ev.state <- snap
	}
}

func (r *Room) handleJoin(req *joinReq) {
	if len(r.sessions) >= r.cfg.MaxPlayers {
		req.reply <- joinResult{err: ErrRoomFull}
		return
	}
	p, err := r.world.AddPlayer(req.name)
	if err != nil {
		req.reply <- joinResult{err: fmt.Errorf("game: join %s: %w", r.id, err)}
		return
	}
	s := newSession(r, p.ID, req.name, req.conn)

	ack, err := protocol.AppendJoinAck(nil, protocol.JoinAck{YourID: uint16(p.ID), Tick: r.world.Tick})
	if err != nil {
		r.world.RemovePlayer(p.ID)
		req.reply <- joinResult{err: fmt.Errorf("game: encode join ack: %w", err)}
		return
	}

	r.sessions[p.ID] = s
	r.setPlayerCount()
	s.enqueue(ack, true)

	r.log.Info("player joined", "player", p.ID, "name", req.name, "addr", req.conn.RemoteAddr())
	req.reply <- joinResult{sess: s}
}

// broadcast sends the full world to every client. Iteration 6 replaces this
// with per-viewport interest management and delta encoding.
func (r *Room) broadcast() {
	r.entities = r.world.AppendEntities(r.entities[:0])
	if len(r.entities) > protocol.MaxEntities {
		// Cannot happen while MaxPlayers stays below the wire limit; the guard
		// keeps a future entity type from silently corrupting the format.
		r.entities = r.entities[:protocol.MaxEntities]
	}
	snap := protocol.Snapshot{Tick: r.world.Tick, Entities: r.entities}

	r.world.Each(func(p *Player) {
		s := r.sessions[p.ID]
		if s == nil {
			return
		}
		// The acknowledged input number is per receiver, so each client gets
		// its own encoding of the same entities.
		snap.LastProcessedSeq = p.LastProcessedSeq
		// One allocation per client per snapshot. That is the price of the
		// temporary JSON codec: the buffer is handed to a writer goroutine, so
		// it cannot be reused here. Iteration 3 brings pooled buffers.
		buf, err := protocol.AppendSnapshot(nil, &snap)
		if err != nil {
			r.log.Error("encode snapshot", "player", p.ID, "err", err)
			return
		}
		if s.enqueue(buf, false) {
			r.cfg.Metrics.SnapshotBytes(len(buf))
		}
	})
}

// dropLaggards disconnects clients that have fallen too far behind. The loop
// itself never waits for them: it only decides they are gone.
func (r *Room) dropLaggards() {
	r.kicked = r.kicked[:0]
	for id, s := range r.sessions {
		if s.lagging(r.cfg.MaxBacklog) {
			r.kicked = append(r.kicked, id)
		}
	}
	for _, id := range r.kicked {
		r.removeSession(id, "too slow")
	}
}

// removeSession releases a player and closes its outbound queue. The room is
// the only sender on that channel, so it is also the only one allowed to close
// it, which is what ends the session's write pump.
func (r *Room) removeSession(id PlayerID, reason string) {
	s, ok := r.sessions[id]
	if !ok {
		return
	}
	delete(r.sessions, id)
	r.world.RemovePlayer(id)
	close(s.out)
	r.setPlayerCount()
	r.log.Info("player left", "player", id, "name", s.name, "reason", reason, "dropped_snapshots", s.dropped)
}

// shutdown releases every session. Sessions notice through their closed queue,
// stop their pumps and close their connections.
func (r *Room) shutdown() {
	for id := range r.sessions {
		r.removeSession(id, "server shutdown")
	}
}

func (r *Room) setPlayerCount() {
	n := len(r.sessions)
	r.players.Store(int32(n))
	r.cfg.Metrics.ConnectedPlayers(n)
}

// event is everything that can reach a room from the outside. It is a tagged
// struct rather than an interface so that posting an input allocates nothing.
type event struct {
	kind  eventKind
	id    PlayerID
	input protocol.Input
	join  *joinReq
	state chan protocol.Snapshot
}

type eventKind uint8

const (
	evJoin eventKind = iota + 1
	evLeave
	evInput
	evState
)

type joinReq struct {
	conn  transport.Conn
	name  string
	reply chan joinResult
}

type joinResult struct {
	sess *Session
	err  error
}
