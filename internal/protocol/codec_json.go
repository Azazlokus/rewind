package protocol

// Кодек итерации 1: JSON в конверте {"t":тип,"d":полезная_нагрузка}.
//
// Этот файл — временный задел, он удаляется в итерации 3, когда бинарная
// раскладка из protocol.go возьмёт верх. Сигнатуры уже имеют форму, нужную
// бинарному кодеку — кодировщики дописывают в буфер вызывающего, декодеры
// заполняют структуру вызывающего — поэтому вне этого пакета при смене
// кодировки ничего не меняется.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

type envelope struct {
	T MsgType         `json:"t"`
	D json.RawMessage `json:"d"`
}

// ClientMessage — декодированное сообщение клиент -> сервер. Type выбирает, какое
// поле полезной нагрузки несёт смысл; структура плоская, чтобы декодирование
// ввода на горячем пути ничего не аллоцировало.
type ClientMessage struct {
	Type  MsgType
	Join  Join
	Input Input
}

// ServerMessage — декодированное сообщение сервер -> клиент, для ботов и тестов.
// Snapshot.Entities переиспользуется между вызовами, если вызывающий передаёт
// ту же структуру обратно.
type ServerMessage struct {
	Type     MsgType
	Snapshot Snapshot
	JoinAck  JoinAck
}

// DecodeClient разбирает одно клиентское сообщение. Никогда не паникует: любой
// кривой, обрезанный или враждебный ввод возвращается как ошибка.
func DecodeClient(data []byte) (ClientMessage, error) {
	var msg ClientMessage
	if len(data) == 0 {
		return msg, ErrEmptyMessage
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return msg, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	msg.Type = env.T
	switch env.T {
	case MsgInput:
		if err := json.Unmarshal(env.D, &msg.Input); err != nil {
			return msg, fmt.Errorf("%w: input: %v", ErrMalformed, err)
		}
	case MsgJoin:
		if err := json.Unmarshal(env.D, &msg.Join); err != nil {
			return msg, fmt.Errorf("%w: join: %v", ErrMalformed, err)
		}
		if len(msg.Join.Name) > MaxNameLen {
			return msg, fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(msg.Join.Name))
		}
		if !utf8.ValidString(msg.Join.Name) {
			return msg, fmt.Errorf("%w: name is not valid UTF-8", ErrMalformed)
		}
	default:
		return msg, fmt.Errorf("%w: 0x%02x", ErrUnknownType, uint8(env.T))
	}
	return msg, nil
}

// DecodeServer разбирает одно серверное сообщение в out, переиспользуя его срезы.
func DecodeServer(data []byte, out *ServerMessage) error {
	if len(data) == 0 {
		return ErrEmptyMessage
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	out.Type = env.T
	switch env.T {
	case MsgSnapshot:
		out.Snapshot.Entities = out.Snapshot.Entities[:0]
		if err := json.Unmarshal(env.D, &out.Snapshot); err != nil {
			return fmt.Errorf("%w: snapshot: %v", ErrMalformed, err)
		}
		if len(out.Snapshot.Entities) > MaxEntities {
			return fmt.Errorf("%w: %d", ErrTooManyEntity, len(out.Snapshot.Entities))
		}
	case MsgJoinAck:
		if err := json.Unmarshal(env.D, &out.JoinAck); err != nil {
			return fmt.Errorf("%w: joinack: %v", ErrMalformed, err)
		}
	default:
		return fmt.Errorf("%w: 0x%02x", ErrUnknownType, uint8(env.T))
	}
	return nil
}

// AppendSnapshot кодирует s в dst и возвращает расширенный буфер.
func AppendSnapshot(dst []byte, s *Snapshot) ([]byte, error) {
	if len(s.Entities) > MaxEntities {
		return dst, fmt.Errorf("%w: %d", ErrTooManyEntity, len(s.Entities))
	}
	return appendEnvelope(dst, MsgSnapshot, s)
}

// AppendJoinAck кодирует a в dst и возвращает расширенный буфер.
func AppendJoinAck(dst []byte, a JoinAck) ([]byte, error) {
	return appendEnvelope(dst, MsgJoinAck, a)
}

// AppendInput кодирует in в dst и возвращает расширенный буфер.
func AppendInput(dst []byte, in Input) ([]byte, error) {
	return appendEnvelope(dst, MsgInput, in)
}

// AppendJoin кодирует j в dst и возвращает расширенный буфер.
func AppendJoin(dst []byte, j Join) ([]byte, error) {
	if len(j.Name) > MaxNameLen {
		return dst, fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(j.Name))
	}
	return appendEnvelope(dst, MsgJoin, j)
}

func appendEnvelope(dst []byte, t MsgType, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return dst, fmt.Errorf("protocol: encode %s: %w", t, err)
	}
	dst = append(dst, `{"t":`...)
	dst = strconv.AppendUint(dst, uint64(t), 10)
	dst = append(dst, `,"d":`...)
	dst = append(dst, body...)
	return append(dst, '}'), nil
}
