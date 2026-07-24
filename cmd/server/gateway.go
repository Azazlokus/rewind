package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"arena/internal/hub"
	"arena/internal/protocol"
	"arena/internal/transport"
)

// gateway upgrades HTTP requests to WebSocket, runs the join handshake and binds
// the connection to a room. It is the one place that speaks both HTTP and the
// game API; everything downstream sees only transport.Conn.
type gateway struct {
	hub    *hub.Hub
	log    *slog.Logger
	wsOpts transport.WSOptions
	// joinTimeout bounds the handshake: a client that connects but never sends
	// a valid Join is dropped rather than tying up a goroutine.
	joinTimeout time.Duration
}

func newGateway(h *hub.Hub, log *slog.Logger, cfg serverConfig) *gateway {
	return &gateway{
		hub:         h,
		log:         log,
		joinTimeout: cfg.JoinTimeout,
		wsOpts: transport.WSOptions{
			// Iteration 1 is JSON over text frames; iteration 3 flips this to
			// transport.KindBinary along with the codec.
			WriteKind:          transport.KindText,
			ReadLimit:          32 << 10,
			WriteTimeout:       5 * time.Second,
			InsecureSkipVerify: cfg.AllowAllOrigin,
		},
	}
}

// ServeHTTP handles a WebSocket upgrade and then serves the session for its whole
// lifetime. It returns only when the client disconnects or the server shuts down.
func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := transport.Upgrade(w, r, g.wsOpts)
	if err != nil {
		// Upgrade has already written a response.
		g.log.Debug("upgrade failed", "addr", r.RemoteAddr, "err", err)
		return
	}

	// The request context ends when this handler returns, which would kill the
	// session immediately. Derive the session lifetime from the server's base
	// context instead, so shutdown — not the HTTP layer — decides when to stop.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	name, err := g.handshake(ctx, conn)
	if err != nil {
		g.log.Debug("handshake failed", "addr", r.RemoteAddr, "err", err)
		_ = conn.Close("handshake failed")
		return
	}

	room, err := g.hub.Assign()
	if err != nil {
		g.log.Warn("assign room failed", "addr", r.RemoteAddr, "err", err)
		_ = conn.Close("no room available")
		return
	}

	sess, err := room.Join(ctx, conn, name)
	if err != nil {
		g.log.Warn("join failed", "room", room.ID(), "err", err)
		_ = conn.Close("join failed")
		return
	}

	// Run blocks until the session ends; a plain disconnect is not an error.
	if err := sess.Run(ctx); err != nil && !errors.Is(err, transport.ErrClosed) {
		g.log.Debug("session ended", "player", sess.ID(), "err", err)
	}
}

// handshake reads the first frame, which must be a Join, and returns the name.
func (g *gateway) handshake(ctx context.Context, conn transport.Conn) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.joinTimeout)
	defer cancel()

	data, err := conn.Read(ctx)
	if err != nil {
		return "", err
	}
	msg, err := protocol.DecodeClient(data)
	if err != nil {
		return "", err
	}
	if msg.Type != protocol.MsgJoin {
		return "", errUnexpectedFirstMessage
	}
	return msg.Join.Name, nil
}

var errUnexpectedFirstMessage = errors.New("gateway: first message was not a join")

// staticHandler serves the web client. It disables caching so the single-file
// client is always fresh during development.
func staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	})
}

// compile-time assertion that gateway satisfies http.Handler.
var _ http.Handler = (*gateway)(nil)
