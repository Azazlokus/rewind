package protocol

// Бинарный кодек v1 (little-endian). Заменил временный JSON итерации 1.
//
// Раскладка сообщений — в шапке пакета (protocol.go). Кодировщики дописывают в
// буфер вызывающего и не аллоцируют при достаточной ёмкости; декодеры читают из
// среза с проверкой границ и никогда не паникуют — любой кривой ввод возвращается
// ошибкой. Горячие пути (encode snapshot, decode input) — zero-alloc, что
// подтверждают бенчмарки с -benchmem.

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

// entitySize — размер одной сущности на проводе:
// [2B id][1B kind][2B x][2B y][2B vx][2B vy][1B hp].
const entitySize = 12

// ClientMessage — декодированное сообщение клиент -> сервер. Type выбирает, какое
// поле несёт смысл; структура плоская, чтобы декодирование ввода на горячем пути
// ничего не аллоцировало.
type ClientMessage struct {
	Type  MsgType
	Join  Join
	Input Input
}

// ServerMessage — декодированное сообщение сервер -> клиент, для ботов и тестов.
// Snapshot.Entities переиспользуется между вызовами, если вызывающий передаёт ту
// же структуру обратно.
type ServerMessage struct {
	Type     MsgType
	Snapshot Snapshot
	JoinAck  JoinAck
	Spawn    Spawn
	Death    Death
	Hit      Hit
}

// DecodeClient разбирает одно клиентское сообщение. Никогда не паникует: любой
// кривой, обрезанный или враждебный ввод возвращается как ошибка.
func DecodeClient(data []byte) (ClientMessage, error) {
	var msg ClientMessage
	if len(data) == 0 {
		return msg, ErrEmptyMessage
	}
	msg.Type = MsgType(data[0])
	body := data[1:]
	switch msg.Type {
	case MsgInput:
		if len(body) < 15 {
			return msg, fmt.Errorf("%w: input needs 15 bytes, got %d", ErrShortMessage, len(body))
		}
		msg.Input.Seq = binary.LittleEndian.Uint32(body[0:4])
		msg.Input.Buttons = body[4]
		msg.Input.Aim = binary.LittleEndian.Uint16(body[5:7])
		msg.Input.ViewTick = binary.LittleEndian.Uint32(body[7:11])
		msg.Input.AckTick = binary.LittleEndian.Uint32(body[11:15])
	case MsgJoin:
		if len(body) < 1 {
			return msg, fmt.Errorf("%w: join length byte", ErrShortMessage)
		}
		n := int(body[0])
		if n > MaxNameLen {
			return msg, fmt.Errorf("%w: %d bytes", ErrNameTooLong, n)
		}
		if len(body) < 1+n {
			return msg, fmt.Errorf("%w: join name %d bytes, got %d", ErrShortMessage, n, len(body)-1)
		}
		name := body[1 : 1+n]
		if !utf8.Valid(name) {
			return msg, fmt.Errorf("%w: name is not valid UTF-8", ErrMalformed)
		}
		msg.Join.Name = string(name)
	default:
		return msg, fmt.Errorf("%w: 0x%02x", ErrUnknownType, uint8(msg.Type))
	}
	return msg, nil
}

// DecodeServer разбирает одно серверное сообщение в out, переиспользуя его срезы.
func DecodeServer(data []byte, out *ServerMessage) error {
	if len(data) == 0 {
		return ErrEmptyMessage
	}
	out.Type = MsgType(data[0])
	body := data[1:]
	switch out.Type {
	case MsgSnapshot:
		if len(body) < 13 {
			return fmt.Errorf("%w: snapshot header", ErrShortMessage)
		}
		out.Snapshot.Tick = binary.LittleEndian.Uint32(body[0:4])
		out.Snapshot.BaseTick = binary.LittleEndian.Uint32(body[4:8])
		out.Snapshot.LastProcessedSeq = binary.LittleEndian.Uint32(body[8:12])
		count := int(body[12])
		body = body[13:]
		if len(body) < count*entitySize {
			return fmt.Errorf("%w: %d entities need %d bytes, got %d",
				ErrShortMessage, count, count*entitySize, len(body))
		}
		ents := out.Snapshot.Entities[:0]
		for i := range count {
			off := i * entitySize
			ents = append(ents, Entity{
				ID:   binary.LittleEndian.Uint16(body[off : off+2]),
				Kind: EntityKind(body[off+2]),
				X:    dequantizeCoord(binary.LittleEndian.Uint16(body[off+3 : off+5])),
				Y:    dequantizeCoord(binary.LittleEndian.Uint16(body[off+5 : off+7])),
				VX:   dequantizeVel(int16(binary.LittleEndian.Uint16(body[off+7 : off+9]))),
				VY:   dequantizeVel(int16(binary.LittleEndian.Uint16(body[off+9 : off+11]))),
				HP:   body[off+11],
			})
		}
		out.Snapshot.Entities = ents
		body = body[count*entitySize:]
		// Список ушедших id (для дельты; у полного снапшота removed == 0).
		if len(body) < 1 {
			return fmt.Errorf("%w: snapshot removed count", ErrShortMessage)
		}
		rcount := int(body[0])
		body = body[1:]
		if len(body) < rcount*2 {
			return fmt.Errorf("%w: %d removed need %d bytes, got %d",
				ErrShortMessage, rcount, rcount*2, len(body))
		}
		rem := out.Snapshot.Removed[:0]
		for i := range rcount {
			rem = append(rem, binary.LittleEndian.Uint16(body[i*2:i*2+2]))
		}
		out.Snapshot.Removed = rem
	case MsgJoinAck:
		if len(body) < 6 {
			return fmt.Errorf("%w: joinack needs 6 bytes, got %d", ErrShortMessage, len(body))
		}
		out.JoinAck.YourID = binary.LittleEndian.Uint16(body[0:2])
		out.JoinAck.Tick = binary.LittleEndian.Uint32(body[2:6])
	case MsgSpawn:
		if len(body) < 6 {
			return fmt.Errorf("%w: spawn needs 6 bytes, got %d", ErrShortMessage, len(body))
		}
		out.Spawn.ID = binary.LittleEndian.Uint16(body[0:2])
		out.Spawn.X = dequantizeCoord(binary.LittleEndian.Uint16(body[2:4]))
		out.Spawn.Y = dequantizeCoord(binary.LittleEndian.Uint16(body[4:6]))
	case MsgDeath:
		if len(body) < 4 {
			return fmt.Errorf("%w: death needs 4 bytes, got %d", ErrShortMessage, len(body))
		}
		out.Death.Victim = binary.LittleEndian.Uint16(body[0:2])
		out.Death.Killer = binary.LittleEndian.Uint16(body[2:4])
	case MsgHit:
		if len(body) < 6 {
			return fmt.Errorf("%w: hit needs 6 bytes, got %d", ErrShortMessage, len(body))
		}
		out.Hit.Attacker = binary.LittleEndian.Uint16(body[0:2])
		out.Hit.Victim = binary.LittleEndian.Uint16(body[2:4])
		out.Hit.Damage = body[4]
		out.Hit.VictimHP = body[5]
	default:
		return fmt.Errorf("%w: 0x%02x", ErrUnknownType, uint8(out.Type))
	}
	return nil
}

// AppendSnapshot кодирует s в dst и возвращает расширенный буфер. Полный снапшот
// и дельта используют один формат: BaseTick==0 — полный, иначе дельта (Entities —
// изменённые/новые, Removed — ушедшие id).
func AppendSnapshot(dst []byte, s *Snapshot) ([]byte, error) {
	if len(s.Entities) > MaxEntities {
		return dst, fmt.Errorf("%w: %d changed", ErrTooManyEntity, len(s.Entities))
	}
	if len(s.Removed) > MaxEntities {
		return dst, fmt.Errorf("%w: %d removed", ErrTooManyEntity, len(s.Removed))
	}
	dst = append(dst, byte(MsgSnapshot))
	dst = binary.LittleEndian.AppendUint32(dst, s.Tick)
	dst = binary.LittleEndian.AppendUint32(dst, s.BaseTick)
	dst = binary.LittleEndian.AppendUint32(dst, s.LastProcessedSeq)
	dst = append(dst, byte(len(s.Entities)))
	for i := range s.Entities {
		e := &s.Entities[i]
		dst = binary.LittleEndian.AppendUint16(dst, e.ID)
		dst = append(dst, byte(e.Kind))
		dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(e.X))
		dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(e.Y))
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVel(e.VX)))
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVel(e.VY)))
		dst = append(dst, e.HP)
	}
	dst = append(dst, byte(len(s.Removed)))
	for _, id := range s.Removed {
		dst = binary.LittleEndian.AppendUint16(dst, id)
	}
	return dst, nil
}

// AppendJoinAck кодирует a в dst и возвращает расширенный буфер.
func AppendJoinAck(dst []byte, a JoinAck) ([]byte, error) {
	dst = append(dst, byte(MsgJoinAck))
	dst = binary.LittleEndian.AppendUint16(dst, a.YourID)
	dst = binary.LittleEndian.AppendUint32(dst, a.Tick)
	return dst, nil
}

// AppendSpawn кодирует Spawn-событие в dst и возвращает расширенный буфер.
func AppendSpawn(dst []byte, s Spawn) ([]byte, error) {
	dst = append(dst, byte(MsgSpawn))
	dst = binary.LittleEndian.AppendUint16(dst, s.ID)
	dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(s.X))
	dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(s.Y))
	return dst, nil
}

// AppendDeath кодирует Death-событие в dst и возвращает расширенный буфер.
func AppendDeath(dst []byte, d Death) ([]byte, error) {
	dst = append(dst, byte(MsgDeath))
	dst = binary.LittleEndian.AppendUint16(dst, d.Victim)
	dst = binary.LittleEndian.AppendUint16(dst, d.Killer)
	return dst, nil
}

// AppendHit кодирует Hit-событие в dst и возвращает расширенный буфер.
func AppendHit(dst []byte, h Hit) ([]byte, error) {
	dst = append(dst, byte(MsgHit))
	dst = binary.LittleEndian.AppendUint16(dst, h.Attacker)
	dst = binary.LittleEndian.AppendUint16(dst, h.Victim)
	dst = append(dst, h.Damage, h.VictimHP)
	return dst, nil
}

// AppendInput кодирует in в dst и возвращает расширенный буфер.
func AppendInput(dst []byte, in Input) ([]byte, error) {
	dst = append(dst, byte(MsgInput))
	dst = binary.LittleEndian.AppendUint32(dst, in.Seq)
	dst = append(dst, in.Buttons)
	dst = binary.LittleEndian.AppendUint16(dst, in.Aim)
	dst = binary.LittleEndian.AppendUint32(dst, in.ViewTick)
	dst = binary.LittleEndian.AppendUint32(dst, in.AckTick)
	return dst, nil
}

// AppendJoin кодирует j в dst и возвращает расширенный буфер.
func AppendJoin(dst []byte, j Join) ([]byte, error) {
	if len(j.Name) > MaxNameLen {
		return dst, fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(j.Name))
	}
	dst = append(dst, byte(MsgJoin), byte(len(j.Name)))
	dst = append(dst, j.Name...)
	return dst, nil
}
