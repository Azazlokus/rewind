package protocol

// Iteration 1 codec: JSON in a {"t":type,"d":payload} envelope.
//
// This file is temporary scaffolding and is deleted in iteration 3, when the
// binary layout documented in protocol.go takes over. The signatures already
// have the shape the binary codec needs — encoders append into a caller-owned
// buffer, decoders fill a caller-owned struct — so nothing outside this package
// changes when the encoding does.

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

// ClientMessage is a decoded client -> server message. Type selects which
// payload field carries meaning; the struct is flat so that decoding a hot-path
// input never allocates.
type ClientMessage struct {
	Type  MsgType
	Join  Join
	Input Input
}

// ServerMessage is a decoded server -> client message, used by bots and tests.
// Snapshot.Entities is reused across calls when the caller passes the same
// struct back in.
type ServerMessage struct {
	Type     MsgType
	Snapshot Snapshot
	JoinAck  JoinAck
}

// DecodeClient parses one client message. It never panics: every malformed,
// truncated or hostile input comes back as an error.
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

// DecodeServer parses one server message into out, reusing its slices.
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

// AppendSnapshot encodes s onto dst and returns the extended buffer.
func AppendSnapshot(dst []byte, s *Snapshot) ([]byte, error) {
	if len(s.Entities) > MaxEntities {
		return dst, fmt.Errorf("%w: %d", ErrTooManyEntity, len(s.Entities))
	}
	return appendEnvelope(dst, MsgSnapshot, s)
}

// AppendJoinAck encodes a onto dst and returns the extended buffer.
func AppendJoinAck(dst []byte, a JoinAck) ([]byte, error) {
	return appendEnvelope(dst, MsgJoinAck, a)
}

// AppendInput encodes in onto dst and returns the extended buffer.
func AppendInput(dst []byte, in Input) ([]byte, error) {
	return appendEnvelope(dst, MsgInput, in)
}

// AppendJoin encodes j onto dst and returns the extended buffer.
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
