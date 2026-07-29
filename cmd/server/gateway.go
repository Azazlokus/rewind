package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"arena/internal/account"
	"arena/internal/hub"
	"arena/internal/protocol"
	"arena/internal/transport"
)

// gateway апгрейдит HTTP-запросы до WebSocket, проводит рукопожатие join и
// привязывает соединение к комнате. Это единственное место, говорящее и на HTTP,
// и на игровом API; всё ниже по течению видит только transport.Conn.
type gateway struct {
	hub *hub.Hub
	// accounts проверяет токен из Join и связывает сессию с аккаунтом (итер. 14B).
	accounts *account.Service
	log      *slog.Logger
	wsOpts   transport.WSOptions
	// joinTimeout ограничивает рукопожатие: клиент, который подключился, но так и
	// не прислал валидный Join, сбрасывается, а не занимает горутину.
	joinTimeout time.Duration
}

func newGateway(h *hub.Hub, accounts *account.Service, log *slog.Logger, cfg serverConfig) *gateway {
	return &gateway{
		hub:         h,
		accounts:    accounts,
		log:         log,
		joinTimeout: cfg.JoinTimeout,
		wsOpts: transport.WSOptions{
			// Итерация 3: бинарный кодек, поэтому и кадры бинарные.
			WriteKind:          transport.KindBinary,
			ReadLimit:          32 << 10,
			WriteTimeout:       5 * time.Second,
			InsecureSkipVerify: cfg.AllowAllOrigin,
		},
	}
}

// ServeHTTP обрабатывает апгрейд до WebSocket, а затем обслуживает сессию всё её
// время жизни. Возвращается только когда клиент отключается или сервер
// выключается.
func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := transport.Upgrade(w, r, g.wsOpts)
	if err != nil {
		// Upgrade уже записал ответ.
		g.log.Debug("upgrade failed", "addr", r.RemoteAddr, "err", err)
		return
	}
	g.serve(r, conn)
}

// serve обслуживает сессию поверх готового Conn (WebSocket или WebRTC DataChannel):
// рукопожатие join, назначение комнаты, затем Run до конца сессии. Транспорт ниже
// не важен — весь код работает через Conn.
func (g *gateway) serve(r *http.Request, conn transport.Conn) {
	// Контекст запроса завершится, когда обработчик вернётся, что мгновенно убило
	// бы сессию. Поэтому время жизни сессии выводим из базового контекста
	// сервера — пусть решает shutdown, а не HTTP-слой.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	join, err := g.handshake(ctx, conn)
	if err != nil {
		g.log.Debug("handshake failed", "addr", r.RemoteAddr, "err", err)
		_ = conn.Close("handshake failed")
		return
	}
	name, accountID := g.resolveIdentity(join)

	room, err := g.hub.Assign()
	if err != nil {
		g.log.Warn("assign room failed", "addr", r.RemoteAddr, "err", err)
		_ = conn.Close("no room available")
		return
	}

	sess, err := room.Join(ctx, conn, name, accountID)
	if err != nil {
		g.log.Warn("join failed", "room", room.ID(), "err", err)
		_ = conn.Close("join failed")
		return
	}

	// Run блокируется до конца сессии; обычный дисконнект не ошибка.
	if err := sess.Run(ctx); err != nil && !errors.Is(err, transport.ErrClosed) {
		g.log.Debug("session ended", "player", sess.ID(), "err", err)
	}
}

// handshake читает первый кадр, который обязан быть Join, и возвращает его.
func (g *gateway) handshake(ctx context.Context, conn transport.Conn) (protocol.Join, error) {
	ctx, cancel := context.WithTimeout(ctx, g.joinTimeout)
	defer cancel()

	data, err := conn.Read(ctx)
	if err != nil {
		return protocol.Join{}, err
	}
	msg, err := protocol.DecodeClient(data)
	if err != nil {
		return protocol.Join{}, err
	}
	if msg.Type != protocol.MsgJoin {
		return protocol.Join{}, errUnexpectedFirstMessage
	}
	return msg.Join, nil
}

// resolveIdentity превращает Join в (имя, accountID) для комнаты. Валидный токен
// авторитетен: имя берётся ИЗ токена (анти-имперсонация — клиент не может назваться
// чужим именем через поле Name), аккаунт привязывается. Пустой/битый/просроченный
// токен — деградация до гостя (accountID 0) с именем из Join; клиент перелогинится
// по REST и переподключится. Никогда не доверяем accountID из непроверенного токена.
func (g *gateway) resolveIdentity(j protocol.Join) (string, int64) {
	if j.Token != "" {
		if id, err := g.accounts.Verify(j.Token); err == nil {
			return id.Name, id.AccountID
		} else {
			g.log.Debug("join token rejected, falling back to guest", "err", err)
		}
	}
	return j.Name, 0
}

var errUnexpectedFirstMessage = errors.New("gateway: first message was not a join")

// staticHandler отдаёт веб-клиент. Кеширование отключено, чтобы одностраничный
// клиент всегда был свежим при разработке.
func staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	})
}

// проверка на этапе компиляции, что gateway удовлетворяет http.Handler.
var _ http.Handler = (*gateway)(nil)
