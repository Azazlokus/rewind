// Package transport carries opaque messages between the server and one client.
//
// The game packages never see a concrete network stack, only Conn. That is what
// makes it possible to add a WebRTC data channel later without touching the
// simulation or the codec.
package transport

import (
	"context"
	"errors"
)

// ErrClosed is reported by Read and Write once the peer or the local side has
// closed the connection. Callers should treat it as a normal end of stream.
var ErrClosed = errors.New("transport: connection closed")

// Conn is a message-oriented, bidirectional connection to one client.
//
// A Conn tolerates one concurrent reader and one concurrent writer. Close may be
// called at any time from any goroutine and unblocks a pending Read and Write.
type Conn interface {
	// Read returns the next message. The returned slice stays valid until the
	// following call to Read, so callers must copy anything they retain.
	Read(ctx context.Context) ([]byte, error)

	// Write sends one message. The implementation must not retain msg after it
	// returns, which lets the caller reuse the buffer.
	Write(ctx context.Context, msg []byte) error

	// Close closes the connection with a human-readable reason. It is safe to
	// call more than once; only the first call has an effect.
	Close(reason string) error

	// RemoteAddr describes the peer, for logs only.
	RemoteAddr() string
}
