package transport

import (
	"context"
	"sync"
)

// Pipe возвращает два связанных in-memory Conn, серверную сторону первой. Это
// основа headless-тестов: комнату можно прогнать сквозь настоящие pump'ы сессии
// и настоящий кодек, без сети.
//
// Каждое направление буферизуется на buffer сообщений. Писатель блокируется,
// когда буфер полон — так имитируется медленный клиент.
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
	closed chan struct{} // закрывается этой стороной
	peer   chan struct{} // закрывается другой стороной
	once   sync.Once
	addr   string
}

func (p *pipeConn) Read(ctx context.Context) ([]byte, error) {
	// Сообщения, уже летящие в момент закрытия пира, всё равно доставляются,
	// чтобы тест не терял последний снапшот из-за гонки с close.
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
	// Conn разрешает вызывающему переиспользовать msg после возврата Write.
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
