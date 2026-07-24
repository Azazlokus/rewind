package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// maxBadMessages is how many undecodable messages a client may send before it is
// disconnected. A few can happen across a version change; a stream of them is a
// broken or hostile client.
const maxBadMessages = 32

// Session is one connected client: a transport, an outbound queue and the two
// pumps that drive them.
//
// Ownership rules, which the whole design rests on:
//   - out is written and closed by the room goroutine only. The write pump is a
//     pure consumer, so "only the sender closes" holds.
//   - the room never blocks on a session. A client that cannot keep up loses
//     snapshots first and the connection second.
type Session struct {
	id   PlayerID
	name string
	conn transport.Conn
	room *Room
	out  chan []byte
	log  *slog.Logger

	// Fields below belong to the room goroutine.
	backlog int    // consecutive sends that had to drop a queued snapshot
	dropped uint64 // total snapshots dropped for this session
	kick    bool   // set when a reliable message could not be queued

	// badMsgs belongs to the read pump goroutine.
	badMsgs int
}

func newSession(r *Room, id PlayerID, name string, conn transport.Conn) *Session {
	return &Session{
		id:   id,
		name: name,
		conn: conn,
		room: r,
		out:  make(chan []byte, r.cfg.SessionQueue),
		log:  r.log,
	}
}

// ID is the player id assigned to this session.
func (s *Session) ID() PlayerID { return s.id }

// Name is the player name.
func (s *Session) Name() string { return s.name }

// Run drives the session until the client disconnects, the room drops it, or ctx
// is cancelled. It returns the error that ended the read side, which is a plain
// disconnect in the common case.
//
// ctx is the caller's lifetime, cancelled on server shutdown. Run derives its
// own child context so that a failing writer tears down the reader as well.
func (s *Session) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		// Whatever ends the write pump also ends the read pump: a session is
		// only useful while both directions work.
		defer cancel()
		s.writePump(runCtx)
	}()

	readErr := s.readPump(runCtx)

	// Tell the room we are gone. This uses the parent context on purpose: the
	// session's own context may already be cancelled by a failed writer, but the
	// room still has to release the player. If the room is shutting down, it
	// closes the session itself and the call returns immediately.
	s.room.leave(ctx, s.id)

	// The room answers the leave, or its shutdown, by closing s.out, which is
	// what lets the write pump finish.
	<-writeDone

	if err := s.conn.Close("session closed"); err != nil {
		s.log.Debug("close connection", "player", s.id, "err", err)
	}
	return readErr
}

// readPump decodes client messages and forwards them to the room. It is the
// only goroutine reading from the connection.
func (s *Session) readPump(ctx context.Context) error {
	for {
		data, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("session %d read: %w", s.id, err)
		}
		msg, err := protocol.DecodeClient(data)
		if err != nil {
			s.badMsgs++
			s.log.Debug("malformed client message", "player", s.id, "err", err)
			if s.badMsgs > maxBadMessages {
				return fmt.Errorf("session %d: %d malformed messages: %w", s.id, s.badMsgs, err)
			}
			continue
		}
		switch msg.Type {
		case protocol.MsgInput:
			s.room.input(ctx, s.id, msg.Input)
		case protocol.MsgJoin:
			// The handshake is over; a second Join is ignored rather than
			// treated as an error, so a reconnecting client is not punished.
		}
	}
}

// writePump copies queued messages to the connection. It ends when the room
// closes the queue, when the connection breaks, or when ctx is cancelled.
func (s *Session) writePump(ctx context.Context) {
	for msg := range s.out {
		if err := s.conn.Write(ctx, msg); err != nil {
			if !errors.Is(err, transport.ErrClosed) && ctx.Err() == nil {
				s.log.Debug("write failed", "player", s.id, "err", err)
			}
			return
		}
	}
}

// enqueue queues one message for the client. It is called by the room goroutine
// only and never blocks.
//
// An unreliable message (a snapshot) gives way to a fresher one: when the queue
// is full the oldest entry is dropped, because a late snapshot is worthless. A
// reliable message (join, spawn, death, hit) cannot be dropped, so a session
// that cannot take it is marked for removal instead.
//
// It reports whether the message was queued.
func (s *Session) enqueue(msg []byte, reliable bool) bool {
	select {
	case s.out <- msg:
		s.backlog = 0
		return true
	default:
	}

	if reliable {
		s.kick = true
		return false
	}

	// Safe without synchronisation: the room is the only sender on this
	// channel, so nothing can refill the slot we are about to free.
	select {
	case <-s.out:
	default:
	}
	s.backlog++
	s.dropped++
	select {
	case s.out <- msg:
		return true
	default:
		return false
	}
}

// lagging reports whether the client has been unable to keep up for long enough
// that the room should disconnect it.
func (s *Session) lagging(maxBacklog int) bool {
	return s.kick || s.backlog > maxBacklog
}
