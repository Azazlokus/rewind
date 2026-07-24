package transport

import (
	"context"
	"sync"
)

// Pipe returns two connected in-memory Conns, server side first. It is the
// backbone of the headless tests: a room can be driven end to end, through the
// real session pumps and the real codec, without a network.
//
// Each direction is buffered by buffer messages. A writer blocks once the buffer
// is full, which is how a slow client is simulated.
func Pipe(buffer int) (Conn, Conn) {
	if buffer < 0 {
		buffer = 0
	}
	toServer := make(chan []byte, buffer)
	toClient := make(chan []byte, buffer)
	serverClosed := make(chan struct{})
	clientClosed := make(chan struct{})

	server := &pipeConn{
		in: toServer, out: toClient,
		closed: serverClosed, peer: clientClosed,
		addr: "pipe/server",
	}
	client := &pipeConn{
		in: toClient, out: toServer,
		closed: clientClosed, peer: serverClosed,
		addr: "pipe/client",
	}
	return server, client
}

type pipeConn struct {
	in     <-chan []byte
	out    chan<- []byte
	closed chan struct{} // closed by this side
	peer   chan struct{} // closed by the other side
	once   sync.Once
	addr   string
}

func (p *pipeConn) Read(ctx context.Context) ([]byte, error) {
	// Messages already in flight when the peer went away are still delivered,
	// so a test never loses the last snapshot to a close race.
	select {
	case msg := <-p.in:
		return msg, nil
	default:
	}
	select {
	case msg := <-p.in:
		return msg, nil
	case <-p.closed:
		return nil, ErrClosed
	case <-p.peer:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pipeConn) Write(ctx context.Context, msg []byte) error {
	// Conn allows the caller to reuse msg after Write returns.
	cp := make([]byte, len(msg))
	copy(cp, msg)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return ErrClosed
	case <-p.peer:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pipeConn) Close(string) error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *pipeConn) RemoteAddr() string { return p.addr }
