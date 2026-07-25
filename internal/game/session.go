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

// reliableQueueSize — глубина очереди reliable-сообщений сессии. Reliable-события
// (JoinAck, а с итерации 5 — Spawn/Death/Hit) редки, но терять их нельзя: буфер
// переживает всплеск, а клиента, который не успевает и его исчерпал,
// отключают.
const reliableQueueSize = 32

// Session — один подключённый клиент: транспорт, две исходящие очереди и два
// pump'а, которые их крутят.
//
// Исходящих очередей две, потому что у сообщений разная ценность:
//   - snapshots — дропаемые: устаревший снапшот бесполезен, при переполнении
//     выкидывается самый старый.
//   - reliable — недропаемые: события, которые клиент обязан увидеть. Клиент,
//     который не может их принять, помечается на удаление, а не теряет событие.
//
// Обе очереди пишет и закрывает только горутина комнаты (правило «закрывает
// только отправитель»); write pump — чистый потребитель. Комната никогда не
// блокируется на сессии: клиент, который не успевает, теряет сначала снапшоты,
// потом соединение.
type Session struct {
	id        PlayerID
	name      string
	conn      transport.Conn
	room      *Room
	reliable  chan []byte
	snapshots chan *[]byte // буферы снапшотов из пула комнаты (возвращаются после записи)
	log       *slog.Logger

	// Поля ниже принадлежат горутине комнаты.
	backlog int    // подряд идущие снапшоты, которым пришлось дропнуть предыдущий
	dropped uint64 // всего снапшотов дропнуто для этой сессии
	kick    bool   // выставлен, когда reliable-очередь переполнилась

	// Дельта-кодирование (итерация 6B), тоже только горутина комнаты: последний
	// подтверждённый клиентом тик и кольцо недавно отправленных наборов как базы.
	ackTick  uint32       // из Input.AckTick; против него кодируется следующий снапшот
	baseline baselineRing // недавно отправленные клиенту наборы сущностей

	// badMsgs принадлежит горутине read pump.
	badMsgs int
}

func newSession(r *Room, id PlayerID, name string, conn transport.Conn) *Session {
	return &Session{
		id:        id,
		name:      name,
		conn:      conn,
		room:      r,
		reliable:  make(chan []byte, reliableQueueSize),
		snapshots: make(chan *[]byte, r.cfg.SessionQueue),
		log:       r.log,
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

	// Комната отвечает на leave (или своим shutdown), закрывая очереди — это и
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

// writePump копирует сообщения из очередей в соединение, отдавая приоритет
// reliable-канал. Завершается, когда комната закрывает обе очереди, когда
// соединение ломается, или когда ctx отменяется.
//
// reliable и snapshots — локальные копии полей: закрытые каналы обнуляем локально
// (nil-канал в select никогда не срабатывает), не трогая поля сессии, которые
// параллельно закрывает горутина комнаты.
func (s *Session) writePump(ctx context.Context) {
	reliable, snapshots := s.reliable, s.snapshots
	// Закрытый канал обнуляем локально; цикл завершается, когда оба обнулены
	// (комната закрыла обе очереди при удалении сессии).
	for reliable != nil || snapshots != nil {
		// Приоритет: сперва вычерпываем всё reliable, не трогая снапшоты.
		select {
		case msg, ok := <-reliable:
			if !ok {
				reliable = nil
			} else if !s.write(ctx, msg) {
				return
			}
			continue
		default:
		}
		// Затем блокируемся на любой из очередей или на отмене контекста.
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-reliable:
			if !ok {
				reliable = nil
			} else if !s.write(ctx, msg) {
				return
			}
		case bp, ok := <-snapshots:
			if !ok {
				snapshots = nil
			} else {
				// Буфер снапшота — из пула комнаты; после записи возвращаем его туда
				// (транспорт данные уже скопировал). Возврат и на ошибке записи: буфер
				// отработал в любом случае.
				wrote := s.write(ctx, *bp)
				s.room.snapPool.Put(bp)
				if !wrote {
					return
				}
			}
		}
	}
}

// write отправляет одно сообщение и сообщает, стоит ли продолжать. Обычный
// дисконнект и отмену контекста не логирует как ошибку.
func (s *Session) write(ctx context.Context, msg []byte) bool {
	if err := s.conn.Write(ctx, msg); err != nil {
		if !errors.Is(err, transport.ErrClosed) && ctx.Err() == nil {
			s.log.Debug("write failed", "player", s.id, "err", err)
		}
		return false
	}
	return true
}

// enqueueSnapshot ставит снапшот в дропаемую очередь. Зовётся только горутиной
// комнаты и никогда не блокируется: когда очередь полна, выкидывается самый
// старый снапшот, потому что запоздалый снапшот бесполезен. Буфер — из пула
// комнаты; выброшенный старый снапшот возвращается в пул. Возвращает, удалось ли
// поставить.
func (s *Session) enqueueSnapshot(bp *[]byte) bool {
	select {
	case s.snapshots <- bp:
		s.backlog = 0
		return true
	default:
	}

	// Безопасно без синхронизации: комната — единственный отправитель в этот
	// канал, поэтому ничто не может снова заполнить слот, который мы освобождаем.
	// Выброшенный буфер возвращаем в пул (write pump его уже не увидит).
	select {
	case old := <-s.snapshots:
		s.room.snapPool.Put(old)
	default:
	}
	s.backlog++
	s.dropped++
	select {
	case s.snapshots <- bp:
		return true
	default:
		return false
	}
}

// enqueueReliable ставит reliable-сообщение в его FIFO. Зовётся только горутиной
// комнаты и никогда не блокируется. Дропать нельзя: если очередь переполнена,
// сессия помечается на удаление. Возвращает, удалось ли поставить.
func (s *Session) enqueueReliable(msg []byte) bool {
	select {
	case s.reliable <- msg:
		return true
	default:
		s.kick = true
		return false
	}
}

// lagging сообщает, отстаёт ли клиент достаточно, чтобы комнате стоило его
// отключить: либо переполнилась reliable-очередь, либо снапшоты дропаются подряд
// слишком долго.
func (s *Session) lagging(maxBacklog int) bool {
	return s.kick || s.backlog > maxBacklog
}
