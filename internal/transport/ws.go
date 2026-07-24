package transport

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Kind выбирает тип WebSocket-кадра для исходящих сообщений. Итерация 1 говорит
// JSON поверх текстовых кадров; бинарный кодек итерации 3 переключит это на
// KindBinary без каких-либо других изменений у вызывающих.
type Kind int

const (
	// KindText шлёт текстовые кадры — в браузере приходят как строки.
	KindText Kind = iota
	// KindBinary шлёт бинарные кадры — в браузере приходят как ArrayBuffer.
	KindBinary
)

func (k Kind) messageType() websocket.MessageType {
	if k == KindBinary {
		return websocket.MessageBinary
	}
	return websocket.MessageText
}

// closeReasonLimit — максимальный размер причины закрытия WebSocket (RFC 6455).
const closeReasonLimit = 123

// WSOptions конфигурирует WebSocket-реализацию Conn.
type WSOptions struct {
	// WriteKind — тип кадра для исходящих сообщений.
	WriteKind Kind
	// ReadLimit ограничивает размер одного входящего сообщения в байтах. Клиент,
	// превысивший его, отключается библиотекой. Ноль — 32 КиБ.
	ReadLimit int64
	// WriteTimeout ограничивает одну операцию Write, чтобы один зависший пир не
	// мог навсегда занять горутину-писатель. Ноль — 5 с.
	WriteTimeout time.Duration
	// OriginPatterns перечисляет разрешённые origin браузера, помимо самого хоста
	// запроса. См. websocket.AcceptOptions.
	OriginPatterns []string
	// InsecureSkipVerify отключает проверку origin. Только для разработки.
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

// Upgrade превращает HTTP-запрос в Conn. При ошибке ответ уже записан, так что
// обработчику остаётся только вернуться.
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

// Dial подключается к WebSocket-серверу. Используется ботами и тестами.
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

// wrapWSError отображает закрытое соединение на ErrClosed, чтобы вызывающий мог
// отличить обычный дисконнект от настоящей ошибки, сохраняя исходную ошибку в
// цепочке.
func wrapWSError(op string, err error) error {
	if websocket.CloseStatus(err) != -1 {
		return fmt.Errorf("transport: %s: %w: %v", op, ErrClosed, err)
	}
	return fmt.Errorf("transport: %s: %w", op, err)
}
