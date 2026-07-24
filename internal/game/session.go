package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// maxBadMessages — сколько недекодируемых сообщений клиент может прислать до
// отключения. Несколько могут случиться при смене версии; поток таких — сломанный
// или враждебный клиент.
const maxBadMessages = 32

// Session — один подключённый клиент: транспорт, исходящая очередь и два pump'а,
// которые их крутят.
//
// Правила владения, на которых держится вся конструкция:
//   - out пишет и закрывает только горутина комнаты. Write pump — чистый
//     потребитель, поэтому правило «закрывает только отправитель» соблюдено.
//   - комната никогда не блокируется на сессии. Клиент, который не успевает,
//     теряет сначала снапшоты, потом соединение.
type Session struct {
	id   PlayerID
	name string
	conn transport.Conn
	room *Room
	out  chan []byte
	log  *slog.Logger

	// Поля ниже принадлежат горутине комнаты.
	backlog int    // подряд идущие отправки, которым пришлось дропнуть снапшот
	dropped uint64 // всего снапшотов дропнуто для этой сессии
	kick    bool   // выставлен, когда reliable-сообщение не удалось поставить в очередь

	// badMsgs принадлежит горутине read pump.
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

// ID — id игрока, назначенный этой сессии.
func (s *Session) ID() PlayerID { return s.id }

// Name — имя игрока.
func (s *Session) Name() string { return s.name }

// Run крутит сессию, пока клиент не отключится, комната не выкинет её, или ctx не
// отменится. Возвращает ошибку, завершившую сторону чтения, — в обычном случае
// это обычный дисконнект.
//
// ctx — время жизни вызывающего, отменяемое при shutdown сервера. Run порождает
// свой дочерний контекст, чтобы падающий писатель ронял и читателя.
func (s *Session) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		// Что бы ни завершило write pump, оно завершает и read pump: сессия
		// полезна, только пока работают оба направления.
		defer cancel()
		s.writePump(runCtx)
	}()

	readErr := s.readPump(runCtx)

	// Сообщаем комнате, что мы ушли. Здесь намеренно используется родительский
	// контекст: собственный контекст сессии, возможно, уже отменён упавшим
	// писателем, но комнате всё равно надо освободить игрока. Если комната
	// выключается, она закрывает сессию сама и вызов возвращается сразу.
	s.room.leave(ctx, s.id)

	// Комната отвечает на leave (или своим shutdown), закрывая s.out — это и
	// позволяет write pump завершиться.
	<-writeDone

	if err := s.conn.Close("session closed"); err != nil {
		s.log.Debug("close connection", "player", s.id, "err", err)
	}
	return readErr
}

// readPump декодирует сообщения клиента и передаёт их комнате. Это единственная
// горутина, читающая из соединения.
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
			// Рукопожатие уже позади; второй Join игнорируется, а не считается
			// ошибкой, чтобы переподключающийся клиент не был наказан.
		}
	}
}

// writePump копирует сообщения из очереди в соединение. Завершается, когда
// комната закрывает очередь, когда соединение ломается, или когда ctx отменяется.
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

// enqueue ставит одно сообщение в очередь клиента. Зовётся только горутиной
// комнаты и никогда не блокируется.
//
// Негарантированное сообщение (снапшот) уступает место более свежему: когда
// очередь полна, самый старый элемент дропается, потому что запоздалый снапшот
// бесполезен. Reliable-сообщение (join, spawn, death, hit) дропать нельзя,
// поэтому сессия, которая не может его принять, вместо этого помечается на
// удаление.
//
// Возвращает, поставлено ли сообщение в очередь.
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

	// Безопасно без синхронизации: комната — единственный отправитель в этот
	// канал, поэтому ничто не может снова заполнить слот, который мы освобождаем.
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

// lagging сообщает, отстаёт ли клиент достаточно долго, чтобы комнате стоило его
// отключить.
func (s *Session) lagging(maxBacklog int) bool {
	return s.kick || s.backlog > maxBacklog
}
