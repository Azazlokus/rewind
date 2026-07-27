// Пакет bot — headless-клиент. Итерация 1 даёт лишь минимум для интеграционного
// end-to-end теста — dial, join, отправка ввода, чтение снапшотов. Итерация 6
// вырастит его в нагрузочный swarm (случайное движение и стрельба), не меняя эту
// поверхность.
package bot

import (
	"context"
	"fmt"
	"sync/atomic"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// Client — одно headless-соединение с сервером.
type Client struct {
	conn transport.Conn
	id   uint16
	tick uint32
	seq  uint32
	// черновик декодирования, переиспользуется, чтобы ReadSnapshot аллоцировал
	// поменьше.
	msg protocol.ServerMessage
	// rc реконструирует полный набор сущностей из дельт (итерация 6B); принадлежит
	// горутине чтения (ReadSnapshot).
	rc *reconstructor
	// ack — последний реконструированный тик, который бот подтверждает серверу.
	// Пишется горутиной чтения, читается горутиной отправки ввода — поэтому atomic
	// (read и write на одном соединении идут в разных горутинах).
	ack atomic.Uint32
}

// Dial подключается к url (например, ws://host/ws), выполняет рукопожатие join с
// заданным именем и возвращается, когда сервер его подтвердил.
func Dial(ctx context.Context, url, name string) (*Client, error) {
	conn, err := transport.Dial(ctx, url, transport.WSOptions{WriteKind: transport.KindBinary})
	if err != nil {
		return nil, err
	}
	join, err := protocol.AppendJoin(nil, protocol.Join{Name: name})
	if err != nil {
		_ = conn.Close("encode join")
		return nil, err
	}
	if err := conn.Write(ctx, join); err != nil {
		_ = conn.Close("write join")
		return nil, fmt.Errorf("bot: send join: %w", err)
	}
	return Attach(ctx, conn)
}

// DialWebRTC подключается по WebRTC-транспорту (итерация 11): signalURL — WS-эндпоинт
// сигналинга (например, ws://host/rtc), поверх которого поднимается DataChannel;
// дальше рукопожатие join такое же, как в Dial. Симметрично Dial (WebSocket) и
// позволяет ботам/e2e гонять WebRTC-путь. Сигналинг после рукопожатия не нужен
// (non-trickle) — закрываем его сразу.
func DialWebRTC(ctx context.Context, signalURL, name string) (*Client, error) {
	sig, err := transport.Dial(ctx, signalURL, transport.WSOptions{WriteKind: transport.KindText})
	if err != nil {
		return nil, err
	}
	conn, err := transport.DialWebRTC(ctx, sig, transport.WebRTCConfig{})
	_ = sig.Close("signaling done")
	if err != nil {
		return nil, err
	}
	join, err := protocol.AppendJoin(nil, protocol.Join{Name: name})
	if err != nil {
		_ = conn.Close("encode join")
		return nil, err
	}
	if err := conn.Write(ctx, join); err != nil {
		_ = conn.Close("write join")
		return nil, fmt.Errorf("bot: send join: %w", err)
	}
	return Attach(ctx, conn)
}

// Attach строит клиента поверх уже открытого соединения, дожидаясь JoinAck. В
// отличие от Dial, сам Join-кадр НЕ шлётся: это для in-process сценариев (например,
// нагрузка через transport.Pipe), где сервер присоединил игрока прямым room.Join, а
// клиенту остаётся лишь принять подтверждение. Соединение переходит во владение
// клиента.
func Attach(ctx context.Context, conn transport.Conn) (*Client, error) {
	c := &Client{conn: conn, rc: newReconstructor()}
	// Первое серверное сообщение обязано быть JoinAck.
	if err := c.readInto(ctx); err != nil {
		_ = conn.Close("read ack")
		return nil, err
	}
	if c.msg.Type != protocol.MsgJoinAck {
		_ = conn.Close("bad handshake")
		return nil, fmt.Errorf("bot: expected JoinAck, got %v", c.msg.Type)
	}
	c.id = c.msg.JoinAck.YourID
	c.tick = c.msg.JoinAck.Tick
	return c, nil
}

// ID — id игрока, назначенный сервером.
func (c *Client) ID() uint16 { return c.id }

// SendInput отправляет одну команду, присваивая следующий порядковый номер и
// подтверждая последний реконструированный снапшот (AckTick) — по нему сервер
// кодирует дельту.
func (c *Client) SendInput(ctx context.Context, buttons uint8, aim uint16) error {
	c.seq++
	buf, err := protocol.AppendInput(nil, protocol.Input{
		Seq: c.seq, Buttons: buttons, Aim: aim, AckTick: c.ack.Load(),
	})
	if err != nil {
		return err
	}
	if err := c.conn.Write(ctx, buf); err != nil {
		return fmt.Errorf("bot: send input: %w", err)
	}
	return nil
}

// Seq — порядковый номер последнего отправленного ввода.
func (c *Client) Seq() uint32 { return c.seq }

// ReadSnapshot блокируется до следующего снапшота, пропуская reliable-события.
// Снапшот реконструируется из дельты в полный набор; возвращённый Snapshot (с
// полным списком Entities) валиден до следующего вызова ReadSnapshot.
func (c *Client) ReadSnapshot(ctx context.Context) (protocol.Snapshot, error) {
	for {
		if err := c.readInto(ctx); err != nil {
			return protocol.Snapshot{}, err
		}
		if c.msg.Type != protocol.MsgSnapshot {
			continue
		}
		ents, ok := c.rc.apply(&c.msg.Snapshot)
		if !ok {
			continue // дельта с неизвестной базой — пропускаем, не подтверждаем
		}
		c.tick = c.msg.Snapshot.Tick
		c.ack.Store(c.tick) // подтвердим этот тик следующим вводом
		return protocol.Snapshot{
			Tick:             c.msg.Snapshot.Tick,
			LastProcessedSeq: c.msg.Snapshot.LastProcessedSeq,
			Entities:         ents,
		}, nil
	}
}

func (c *Client) readInto(ctx context.Context) error {
	data, err := c.conn.Read(ctx)
	if err != nil {
		return err
	}
	if err := protocol.DecodeServer(data, &c.msg); err != nil {
		return fmt.Errorf("bot: decode: %w", err)
	}
	return nil
}

// Close закрывает соединение.
func (c *Client) Close() error { return c.conn.Close("bot closed") }
