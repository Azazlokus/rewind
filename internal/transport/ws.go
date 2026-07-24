package transport

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Kind selects the WebSocket frame type used for outgoing messages. Iteration 1
// speaks JSON over text frames; the binary codec of iteration 3 switches this to
// KindBinary without any other change to the callers.
type Kind int

const (
	// KindText sends text frames, which arrive as strings in the browser.
	KindText Kind = iota
	// KindBinary sends binary frames, which arrive as ArrayBuffers.
	KindBinary
)

func (k Kind) messageType() websocket.MessageType {
	if k == KindBinary {
		return websocket.MessageBinary
	}
	return websocket.MessageText
}

// closeReasonLimit is the maximum size of a WebSocket close reason (RFC 6455).
const closeReasonLimit = 123

// WSOptions configures the WebSocket implementation of Conn.
type WSOptions struct {
	// WriteKind is the frame type used for outgoing messages.
	WriteKind Kind
	// ReadLimit caps the size of a single inbound message in bytes. A client
	// that exceeds it is disconnected by the library. Zero means 32 KiB.
	ReadLimit int64
	// WriteTimeout bounds a single Write so that one wedged peer cannot pin a
	// writer goroutine forever. Zero means 5s.
	WriteTimeout time.Duration
	// OriginPatterns lists browser origins allowed to connect, in addition to
	// the request host itself. See websocket.AcceptOptions.
	OriginPatterns []string
	// InsecureSkipVerify disables origin checking. Development only.
	InsecureSkipVerify bool
}

func (o WSOptions) withDefaults() WSOptions {
	if o.ReadLimit <= 0 {
		o.ReadLimit = 32 << 10
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 5 * time.Second
	}
	return o
}

// Upgrade turns an HTTP request into a Conn. On error it has already written a
// response, so the handler should just return.
func Upgrade(w http.ResponseWriter, r *http.Request, opts WSOptions) (Conn, error) {
	opts = opts.withDefaults()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:     opts.OriginPatterns,
		InsecureSkipVerify: opts.InsecureSkipVerify,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("transport: accept websocket: %w", err)
	}
	return newWSConn(c, r.RemoteAddr, opts), nil
}

// Dial connects to a WebSocket server. It is used by bots and tests.
func Dial(ctx context.Context, url string, opts WSOptions) (Conn, error) {
	opts = opts.withDefaults()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("transport: dial %s: %w", url, err)
	}
	return newWSConn(c, url, opts), nil
}

type wsConn struct {
	c            *websocket.Conn
	writeType    websocket.MessageType
	writeTimeout time.Duration
	addr         string
	closeOnce    sync.Once
	closeErr     error
}

func newWSConn(c *websocket.Conn, addr string, opts WSOptions) *wsConn {
	c.SetReadLimit(opts.ReadLimit)
	return &wsConn{
		c:            c,
		writeType:    opts.WriteKind.messageType(),
		writeTimeout: opts.WriteTimeout,
		addr:         addr,
	}
}

func (w *wsConn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := w.c.Read(ctx)
	if err != nil {
		return nil, wrapWSError("read", err)
	}
	return data, nil
}

func (w *wsConn) Write(ctx context.Context, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, w.writeTimeout)
	defer cancel()
	if err := w.c.Write(ctx, w.writeType, msg); err != nil {
		return wrapWSError("write", err)
	}
	return nil
}

func (w *wsConn) Close(reason string) error {
	w.closeOnce.Do(func() {
		if len(reason) > closeReasonLimit {
			reason = reason[:closeReasonLimit]
		}
		if err := w.c.Close(websocket.StatusNormalClosure, reason); err != nil {
			w.closeErr = wrapWSError("close", err)
		}
	})
	return w.closeErr
}

func (w *wsConn) RemoteAddr() string { return w.addr }

// wrapWSError maps a shut-down connection onto ErrClosed so that callers can
// tell an ordinary disconnect apart from a real failure, while keeping the
// original error in the chain.
func wrapWSError(op string, err error) error {
	if websocket.CloseStatus(err) != -1 {
		return fmt.Errorf("transport: %s: %w: %v", op, ErrClosed, err)
	}
	return fmt.Errorf("transport: %s: %w", op, err)
}
