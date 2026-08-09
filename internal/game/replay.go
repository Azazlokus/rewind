package game

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"arena/internal/protocol"
)

// Реплеи (итерация 7). Комната пишет лог: seed мира и все принятые события
// (join/leave/input) со ШТАМПОМ ТИКА. Штамп обязателен: World.Step осушает вводы
// пачками — все, что пришли за тик, — поэтому реплей обязан подать те же события в
// те же тики перед тем же Step, иначе разойдётся пер-тиковая сверка и итоговый
// Checksum. cmd/replay (итерация 7B) проигрывает лог headless и сверяет хэш; каждый
// пойманный desync превращается в replay-файл и регрессионный тест.
//
// Записываются только ПРИНЯТЫЕ вводы (прошедшие дедуп в EnqueueInput): дропнутый
// повтор/реордер не меняет lastQueuedSeq, поэтому его отсутствие в логе не влияет
// на воспроизводимость — лог короче, а результат тот же.

// Формат лога на проводе (little-endian):
//
//	[4B magic "ARPL"][1B version][8B seed int64][4B tickRate][4B tickCount]
//	[1B teamMode (только v2)][4B eventCount] затем eventCount событий:
//	  [4B tick][1B kind]
//	    join(1):  [1B nameLen][name UTF-8, ≤16B]
//	    leave(2): [2B id]
//	    input(3): [2B id][4B seq][1B buttons][2B aim][4B viewTick][4B ackTick]
//
// v2 (итер. 23) добавил байт teamMode перед eventCount — командный режим меняет
// коллизию (дружественный огонь), поэтому реплей обязан его знать. v1 (без байта)
// читается как teamMode=false для обратной совместимости.
var replayMagic = [4]byte{'A', 'R', 'P', 'L'}

const replayVersion = 2

// Ошибки декодера лога. Как и кодек протокола, декодер никогда не паникует.
var (
	ErrReplayMagic    = errors.New("replay: bad magic")
	ErrReplayVersion  = errors.New("replay: unsupported version")
	ErrReplayShort    = errors.New("replay: truncated log")
	ErrReplayKind     = errors.New("replay: unknown event kind")
	ErrReplayName     = errors.New("replay: name too long")
	ErrReplayTickRate = errors.New("replay: tickRate must be positive")
)

type replayKind uint8

const (
	replayJoin  replayKind = 1
	replayLeave replayKind = 2
	replayInput replayKind = 3
)

// replayEvent — одно записанное событие со штампом тика (значение w.Tick в момент
// применения, до Step этого тика).
type replayEvent struct {
	tick uint32
	kind replayKind
	id   PlayerID       // leave/input
	name string         // join
	in   protocol.Input // input
}

// ReplayLog — записанная сессия: seed и тикрейт мира, число тиков и события со
// штампом тика в порядке применения.
type ReplayLog struct {
	Seed     int64
	TickRate int
	Ticks    uint32
	TeamMode bool // командный режим (итер. 23, v2)
	events   []replayEvent
}

// Len сообщает число записанных событий.
func (l *ReplayLog) Len() int { return len(l.events) }

func (l *ReplayLog) join(tick uint32, name string) {
	l.events = append(l.events, replayEvent{tick: tick, kind: replayJoin, name: name})
}

func (l *ReplayLog) leave(tick uint32, id PlayerID) {
	l.events = append(l.events, replayEvent{tick: tick, kind: replayLeave, id: id})
}

func (l *ReplayLog) input(tick uint32, id PlayerID, in protocol.Input) {
	l.events = append(l.events, replayEvent{tick: tick, kind: replayInput, id: id, in: in})
}

// Encode сериализует лог в бинарный формат.
func (l *ReplayLog) Encode() []byte {
	dst := make([]byte, 0, 25+len(l.events)*16)
	dst = append(dst, replayMagic[:]...)
	dst = append(dst, replayVersion)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(l.Seed))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(l.TickRate))
	dst = binary.LittleEndian.AppendUint32(dst, l.Ticks)
	teamMode := byte(0)
	if l.TeamMode {
		teamMode = 1
	}
	dst = append(dst, teamMode) // v2 (итер. 23)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(l.events)))
	for i := range l.events {
		e := &l.events[i]
		dst = binary.LittleEndian.AppendUint32(dst, e.tick)
		dst = append(dst, byte(e.kind))
		switch e.kind {
		case replayJoin:
			dst = append(dst, byte(len(e.name)))
			dst = append(dst, e.name...)
		case replayLeave:
			dst = binary.LittleEndian.AppendUint16(dst, uint16(e.id))
		case replayInput:
			dst = binary.LittleEndian.AppendUint16(dst, uint16(e.id))
			dst = binary.LittleEndian.AppendUint32(dst, e.in.Seq)
			dst = append(dst, e.in.Buttons)
			dst = binary.LittleEndian.AppendUint16(dst, e.in.Aim)
			dst = binary.LittleEndian.AppendUint32(dst, e.in.ViewTick)
			dst = binary.LittleEndian.AppendUint32(dst, e.in.AckTick)
		}
	}
	return dst
}

// DecodeReplay разбирает лог из бинарного формата. Никогда не паникует: любой
// кривой/обрезанный ввод возвращается ошибкой (фаззится в итерации 7B).
func DecodeReplay(data []byte) (*ReplayLog, error) {
	if len(data) < 25 {
		return nil, fmt.Errorf("%w: header needs 25 bytes, got %d", ErrReplayShort, len(data))
	}
	if [4]byte(data[0:4]) != replayMagic {
		return nil, ErrReplayMagic
	}
	ver := data[4]
	if ver != 1 && ver != 2 {
		return nil, fmt.Errorf("%w: %d", ErrReplayVersion, ver)
	}
	log := &ReplayLog{
		Seed:     int64(binary.LittleEndian.Uint64(data[5:13])),
		TickRate: int(binary.LittleEndian.Uint32(data[13:17])),
		Ticks:    binary.LittleEndian.Uint32(data[17:21]),
	}
	if log.TickRate <= 0 {
		// Реальный лог всегда пишет положительный тикрейт; 0 сломал бы dt (деление).
		return nil, fmt.Errorf("%w: %d", ErrReplayTickRate, log.TickRate)
	}
	// v2 (итер. 23): байт teamMode перед eventCount; header на 1 байт длиннее. v1 без него.
	headerLen := 25
	if ver >= 2 {
		if len(data) < 26 {
			return nil, fmt.Errorf("%w: v2 header needs 26 bytes, got %d", ErrReplayShort, len(data))
		}
		log.TeamMode = data[21] != 0
		headerLen = 26
	}
	count := binary.LittleEndian.Uint32(data[headerLen-4 : headerLen])
	body := data[headerLen:]
	off := 0
	// need проверяет, что в body осталось n байт с текущей позиции off.
	need := func(n int) bool { return off+n <= len(body) }
	for range count {
		if !need(5) {
			return nil, fmt.Errorf("%w: event header", ErrReplayShort)
		}
		ev := replayEvent{
			tick: binary.LittleEndian.Uint32(body[off : off+4]),
			kind: replayKind(body[off+4]),
		}
		off += 5
		switch ev.kind {
		case replayJoin:
			if !need(1) {
				return nil, fmt.Errorf("%w: join length", ErrReplayShort)
			}
			n := int(body[off])
			off++
			if n > protocol.MaxNameLen {
				return nil, fmt.Errorf("%w: %d", ErrReplayName, n)
			}
			if !need(n) {
				return nil, fmt.Errorf("%w: join name", ErrReplayShort)
			}
			ev.name = string(body[off : off+n])
			off += n
		case replayLeave:
			if !need(2) {
				return nil, fmt.Errorf("%w: leave id", ErrReplayShort)
			}
			ev.id = PlayerID(binary.LittleEndian.Uint16(body[off : off+2]))
			off += 2
		case replayInput:
			if !need(17) {
				return nil, fmt.Errorf("%w: input body", ErrReplayShort)
			}
			ev.id = PlayerID(binary.LittleEndian.Uint16(body[off : off+2]))
			ev.in.Seq = binary.LittleEndian.Uint32(body[off+2 : off+6])
			ev.in.Buttons = body[off+6]
			ev.in.Aim = binary.LittleEndian.Uint16(body[off+7 : off+9])
			ev.in.ViewTick = binary.LittleEndian.Uint32(body[off+9 : off+13])
			ev.in.AckTick = binary.LittleEndian.Uint32(body[off+13 : off+17])
			off += 17
		default:
			return nil, fmt.Errorf("%w: 0x%02x", ErrReplayKind, byte(ev.kind))
		}
		log.events = append(log.events, ev)
	}
	return log, nil
}

// tickDt — длительность тика в секундах как float32. Считается тем же выражением,
// что и в Room.Run, чтобы реплей шагал мир байт-в-байт тем же dt.
func tickDt(tickRate int) float32 {
	return float32((time.Second / time.Duration(tickRate)).Seconds())
}

// Replay реконструирует мир из лога и проигрывает его headless: на каждом тике
// применяет события с этим штампом (в записанном порядке), затем Step. Возвращает
// Checksum итогового мира — его сверяют с эталоном (cmd/replay, регресс-тесты).
//
// Порядок точно повторяет живую комнату: события применяются в drainInbox при
// w.Tick == t, затем идёт Step (t → t+1).
func Replay(log *ReplayLog) (uint64, error) {
	if log.TickRate <= 0 {
		return 0, fmt.Errorf("%w: %d", ErrReplayTickRate, log.TickRate)
	}
	w := NewWorld(log.Seed)
	w.SetTeamMode(log.TeamMode) // до первого join — команды раздаются при входе (итер. 23)
	dt := tickDt(log.TickRate)

	apply := func(e *replayEvent) error {
		switch e.kind {
		case replayJoin:
			if _, err := w.AddPlayer(e.name); err != nil {
				return fmt.Errorf("replay: tick %d join %q: %w", e.tick, e.name, err)
			}
		case replayLeave:
			w.RemovePlayer(e.id)
		case replayInput:
			w.EnqueueInput(e.id, e.in)
		}
		return nil
	}

	i := 0
	for t := uint32(0); t < log.Ticks; t++ {
		for i < len(log.events) && log.events[i].tick == t {
			if err := apply(&log.events[i]); err != nil {
				return 0, err
			}
			i++
		}
		w.Step(dt)
	}
	// Хвостовые события со штампом >= Ticks записаны после последнего Step (обычно
	// leave при остановке комнаты) — применяем без Step, чтобы реплей повторял
	// итоговое состояние мира точно, включая уход игроков.
	for ; i < len(log.events); i++ {
		if err := apply(&log.events[i]); err != nil {
			return 0, err
		}
	}
	return w.Checksum(), nil
}
