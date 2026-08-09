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

// entitySize — размер одной сущности в ПОЛНОМ снапшоте на проводе:
// [2B id][1B kind][2B x][2B y][2B vx][2B vy][1B hp].
const entitySize = 12

// deltaFieldBytes — размер полей, присутствующих по маске m, в дельта-записи
// сущности (без учёта [2B id][1B маска]). Порядок и размеры зеркалит web/game.js.
func deltaFieldBytes(m uint8) int {
	n := 0
	if m&FieldKind != 0 {
		n++
	}
	if m&FieldX != 0 {
		n += 2
	}
	if m&FieldY != 0 {
		n += 2
	}
	if m&FieldVX != 0 {
		n += 2
	}
	if m&FieldVY != 0 {
		n += 2
	}
	if m&FieldHP != 0 {
		n++
	}
	return n
}

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
	Type        MsgType
	Snapshot    Snapshot
	JoinAck     JoinAck
	Spawn       Spawn
	Death       Death
	Hit         Hit
	MatchState  MatchState
	PickupState PickupState
	Killstreak  Killstreak
	WeaponState WeaponState
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
		// Actions (итер. 27) — опциональный завершающий байт: старый ввод (15 байт тела)
		// декодируется с actions=0. Терминальный, как флаг спектатора в Join.
		if len(body) >= 16 {
			msg.Input.Actions = body[15]
		}
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
		// Токен-сессия (итер. 14B): [2B tokenLen][token]. Идёт следом за именем.
		rest := body[1+n:]
		if len(rest) < 2 {
			return msg, fmt.Errorf("%w: join token length", ErrShortMessage)
		}
		tn := int(binary.LittleEndian.Uint16(rest[0:2]))
		if tn > MaxTokenLen {
			return msg, fmt.Errorf("%w: %d bytes", ErrTokenTooLong, tn)
		}
		if len(rest) < 2+tn {
			return msg, fmt.Errorf("%w: join token %d bytes, got %d", ErrShortMessage, tn, len(rest)-2)
		}
		token := rest[2 : 2+tn]
		if !utf8.Valid(token) {
			return msg, fmt.Errorf("%w: token is not valid UTF-8", ErrMalformed)
		}
		msg.Join.Token = string(token)
		// Опциональный флаг спектатора (итер. 22): байт после токена. Старый формат
		// (без байта) декодируется как обычный игрок. Байт терминальный: любой ненулевой
		// = наблюдатель, что за ним — игнорируется (forward-compat, как прежняя терпимость
		// Join к хвосту).
		if len(rest) > 2+tn && rest[2+tn] != 0 {
			msg.Join.Spectator = true
		}
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
		ents := out.Snapshot.Entities[:0]
		masks := out.Snapshot.Masks[:0]
		if out.Snapshot.BaseTick == 0 {
			// Полный снапшот: фиксированные 12-байтные записи.
			if len(body) < count*entitySize {
				return fmt.Errorf("%w: %d entities need %d bytes, got %d",
					ErrShortMessage, count, count*entitySize, len(body))
			}
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
			body = body[count*entitySize:]
			masks = masks[:0] // полный снапшот масок не несёт
		} else {
			// Дельта: [2B id][1B маска][присутствующие поля] в порядке kind/x/y/vx/vy/hp
			// (field-level, итерация 9). Размер полей считаем из маски и проверяем
			// границу один раз на сущность.
			for range count {
				if len(body) < 3 {
					return fmt.Errorf("%w: delta entity header", ErrShortMessage)
				}
				e := Entity{ID: binary.LittleEndian.Uint16(body[0:2])}
				m := body[2] & FieldAll // неизвестные биты отбрасываем
				body = body[3:]
				need := deltaFieldBytes(m)
				if len(body) < need {
					return fmt.Errorf("%w: delta entity fields need %d, got %d",
						ErrShortMessage, need, len(body))
				}
				off := 0
				if m&FieldKind != 0 {
					e.Kind = EntityKind(body[off])
					off++
				}
				if m&FieldX != 0 {
					e.X = dequantizeCoord(binary.LittleEndian.Uint16(body[off : off+2]))
					off += 2
				}
				if m&FieldY != 0 {
					e.Y = dequantizeCoord(binary.LittleEndian.Uint16(body[off : off+2]))
					off += 2
				}
				if m&FieldVX != 0 {
					e.VX = dequantizeVel(int16(binary.LittleEndian.Uint16(body[off : off+2])))
					off += 2
				}
				if m&FieldVY != 0 {
					e.VY = dequantizeVel(int16(binary.LittleEndian.Uint16(body[off : off+2])))
					off += 2
				}
				if m&FieldHP != 0 {
					e.HP = body[off]
				}
				body = body[need:]
				ents = append(ents, e)
				masks = append(masks, m)
			}
		}
		out.Snapshot.Entities = ents
		out.Snapshot.Masks = masks
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
	case MsgMatchState:
		if len(body) < 9 {
			return fmt.Errorf("%w: matchstate header", ErrShortMessage)
		}
		out.MatchState.Phase = body[0]
		out.MatchState.Remaining = binary.LittleEndian.Uint32(body[1:5])
		out.MatchState.Winner = binary.LittleEndian.Uint16(body[5:7])
		out.MatchState.TeamMode = body[7]&matchFlagTeamMode != 0 // флаги (итер. 23)
		count := int(body[8])
		body = body[9:]
		scores := out.MatchState.Scores[:0]
		for range count {
			// Фикс. часть строки табло — 8 байт: [2B id][2B kills][2B deaths][1B team][1B nameLen].
			if len(body) < 8 {
				return fmt.Errorf("%w: matchstate score header", ErrShortMessage)
			}
			s := MatchScore{
				ID:     binary.LittleEndian.Uint16(body[0:2]),
				Kills:  binary.LittleEndian.Uint16(body[2:4]),
				Deaths: binary.LittleEndian.Uint16(body[4:6]),
				Team:   body[6],
			}
			n := int(body[7])
			if n > MaxNameLen {
				return fmt.Errorf("%w: score name %d bytes", ErrNameTooLong, n)
			}
			if len(body) < 8+n {
				return fmt.Errorf("%w: score name %d bytes, got %d", ErrShortMessage, n, len(body)-8)
			}
			name := body[8 : 8+n]
			if !utf8.Valid(name) {
				return fmt.Errorf("%w: score name is not valid UTF-8", ErrMalformed)
			}
			s.Name = string(name)
			scores = append(scores, s)
			body = body[8+n:]
		}
		out.MatchState.Scores = scores
	case MsgPickupState:
		if len(body) < 1 {
			return fmt.Errorf("%w: pickupstate count", ErrShortMessage)
		}
		count := int(body[0])
		body = body[1:]
		if len(body) < count*2 {
			return fmt.Errorf("%w: %d pickups need %d bytes, got %d",
				ErrShortMessage, count, count*2, len(body))
		}
		active := out.PickupState.Active[:0]
		for i := range count {
			active = append(active, Pickup{Spot: body[i*2], Kind: body[i*2+1]})
		}
		out.PickupState.Active = active
	case MsgKillstreak:
		if len(body) < 4 {
			return fmt.Errorf("%w: killstreak needs 4 bytes, got %d", ErrShortMessage, len(body))
		}
		out.Killstreak.ID = binary.LittleEndian.Uint16(body[0:2])
		out.Killstreak.Streak = binary.LittleEndian.Uint16(body[2:4])
	case MsgWeaponState:
		if len(body) < 1 {
			return fmt.Errorf("%w: weaponstate count", ErrShortMessage)
		}
		count := int(body[0])
		body = body[1:]
		if len(body) < count*3 {
			return fmt.Errorf("%w: %d weapons need %d bytes, got %d",
				ErrShortMessage, count, count*3, len(body))
		}
		weapons := out.WeaponState.Weapons[:0]
		for i := range count {
			off := i * 3
			weapons = append(weapons, WeaponInfo{
				ID:     binary.LittleEndian.Uint16(body[off : off+2]),
				Weapon: body[off+2],
			})
		}
		out.WeaponState.Weapons = weapons
	default:
		return fmt.Errorf("%w: 0x%02x", ErrUnknownType, uint8(out.Type))
	}
	return nil
}

// AppendSnapshot кодирует s в dst и возвращает расширенный буфер. BaseTick==0 —
// полный снапшот: каждая сущность идёт фиксированной 12-байтной записью. BaseTick!=0
// — дельта: каждая изменённая сущность идёт как [2B id][1B маска][только
// присутствующие поля] (field-level, итерация 9), поэтому дельта требует Masks
// параллельно Entities. Removed несёт id ушедших (только дельта).
func AppendSnapshot(dst []byte, s *Snapshot) ([]byte, error) {
	if len(s.Entities) > MaxEntities {
		return dst, fmt.Errorf("%w: %d changed", ErrTooManyEntity, len(s.Entities))
	}
	if len(s.Removed) > MaxEntities {
		return dst, fmt.Errorf("%w: %d removed", ErrTooManyEntity, len(s.Removed))
	}
	if s.BaseTick != 0 && len(s.Masks) != len(s.Entities) {
		return dst, fmt.Errorf("%w: delta has %d entities but %d masks",
			ErrMalformed, len(s.Entities), len(s.Masks))
	}
	dst = append(dst, byte(MsgSnapshot))
	dst = binary.LittleEndian.AppendUint32(dst, s.Tick)
	dst = binary.LittleEndian.AppendUint32(dst, s.BaseTick)
	dst = binary.LittleEndian.AppendUint32(dst, s.LastProcessedSeq)
	dst = append(dst, byte(len(s.Entities)))
	if s.BaseTick == 0 {
		for i := range s.Entities {
			dst = appendEntityFull(dst, &s.Entities[i])
		}
	} else {
		for i := range s.Entities {
			dst = appendEntityDelta(dst, &s.Entities[i], s.Masks[i])
		}
	}
	dst = append(dst, byte(len(s.Removed)))
	for _, id := range s.Removed {
		dst = binary.LittleEndian.AppendUint16(dst, id)
	}
	return dst, nil
}

// appendEntityFull пишет сущность целиком (12 байт) — раскладка полного снапшота.
func appendEntityFull(dst []byte, e *Entity) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, e.ID)
	dst = append(dst, byte(e.Kind))
	dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(e.X))
	dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(e.Y))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVel(e.VX)))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVel(e.VY)))
	return append(dst, e.HP)
}

// appendEntityDelta пишет изменённую сущность как [2B id][1B маска][присутствующие
// поля] в порядке kind/x/y/vx/vy/hp — раскладка дельты (итерация 9). Неизвестные
// биты маски игнорируются: пишем только определённые поля.
func appendEntityDelta(dst []byte, e *Entity, m uint8) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, e.ID)
	dst = append(dst, m)
	if m&FieldKind != 0 {
		dst = append(dst, byte(e.Kind))
	}
	if m&FieldX != 0 {
		dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(e.X))
	}
	if m&FieldY != 0 {
		dst = binary.LittleEndian.AppendUint16(dst, quantizeCoord(e.Y))
	}
	if m&FieldVX != 0 {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVel(e.VX)))
	}
	if m&FieldVY != 0 {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVel(e.VY)))
	}
	if m&FieldHP != 0 {
		dst = append(dst, e.HP)
	}
	return dst
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

// AppendMatchState кодирует состояние матча в dst (итерация 14, +команды итер. 23).
// Раскладка: [1B type][1B phase][4B remaining][2B winner][1B flags][1B scoreCount]
// затем scoreCount × [2B id][2B kills][2B deaths][1B team][1B nameLen][name]. Имена —
// валидные UTF-8 ≤ MaxNameLen (инвариант join), длиннее — ошибка.
func AppendMatchState(dst []byte, m MatchState) ([]byte, error) {
	if len(m.Scores) > MaxEntities {
		return dst, fmt.Errorf("%w: %d scores", ErrTooManyEntity, len(m.Scores))
	}
	dst = append(dst, byte(MsgMatchState))
	dst = append(dst, m.Phase)
	dst = binary.LittleEndian.AppendUint32(dst, m.Remaining)
	dst = binary.LittleEndian.AppendUint16(dst, m.Winner)
	var flags uint8
	if m.TeamMode {
		flags |= matchFlagTeamMode
	}
	dst = append(dst, flags)
	dst = append(dst, byte(len(m.Scores)))
	for i := range m.Scores {
		s := &m.Scores[i]
		if len(s.Name) > MaxNameLen {
			return dst, fmt.Errorf("%w: score name %d bytes", ErrNameTooLong, len(s.Name))
		}
		dst = binary.LittleEndian.AppendUint16(dst, s.ID)
		dst = binary.LittleEndian.AppendUint16(dst, s.Kills)
		dst = binary.LittleEndian.AppendUint16(dst, s.Deaths)
		dst = append(dst, s.Team)
		dst = append(dst, byte(len(s.Name)))
		dst = append(dst, s.Name...)
	}
	return dst, nil
}

// AppendPickupState кодирует состояние пикапов в dst (итерация 19). Раскладка:
// [1B type][1B count] затем count × [1B spot][1B kind]. Полный набор активных
// точек; клиент, приняв его, помечает пустыми все точки вне списка.
func AppendPickupState(dst []byte, p PickupState) ([]byte, error) {
	if len(p.Active) > MaxEntities {
		return dst, fmt.Errorf("%w: %d pickups", ErrTooManyEntity, len(p.Active))
	}
	dst = append(dst, byte(MsgPickupState))
	dst = append(dst, byte(len(p.Active)))
	for _, pk := range p.Active {
		dst = append(dst, pk.Spot, pk.Kind)
	}
	return dst, nil
}

// AppendKillstreak кодирует событие серии убийств в dst (итерация 20). Раскладка:
// [1B type][2B id][2B streak].
func AppendKillstreak(dst []byte, k Killstreak) ([]byte, error) {
	dst = append(dst, byte(MsgKillstreak))
	dst = binary.LittleEndian.AppendUint16(dst, k.ID)
	dst = binary.LittleEndian.AppendUint16(dst, k.Streak)
	return dst, nil
}

// AppendWeaponState кодирует оружие игроков в dst (итер. 26). Раскладка:
// [1B type][1B count] затем count × [2B id][1B weapon]. Полный набор; клиент, приняв
// его, обновляет карту id→оружие.
func AppendWeaponState(dst []byte, ws WeaponState) ([]byte, error) {
	if len(ws.Weapons) > MaxEntities {
		return dst, fmt.Errorf("%w: %d weapons", ErrTooManyEntity, len(ws.Weapons))
	}
	dst = append(dst, byte(MsgWeaponState))
	dst = append(dst, byte(len(ws.Weapons)))
	for _, wi := range ws.Weapons {
		dst = binary.LittleEndian.AppendUint16(dst, wi.ID)
		dst = append(dst, wi.Weapon)
	}
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
	dst = append(dst, in.Actions) // действия/абилки (итер. 27)
	return dst, nil
}

// AppendJoin кодирует j в dst и возвращает расширенный буфер.
func AppendJoin(dst []byte, j Join) ([]byte, error) {
	if len(j.Name) > MaxNameLen {
		return dst, fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(j.Name))
	}
	if len(j.Token) > MaxTokenLen {
		return dst, fmt.Errorf("%w: %d bytes", ErrTokenTooLong, len(j.Token))
	}
	dst = append(dst, byte(MsgJoin), byte(len(j.Name)))
	dst = append(dst, j.Name...)
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(j.Token)))
	dst = append(dst, j.Token...)
	spec := byte(0)
	if j.Spectator {
		spec = 1
	}
	dst = append(dst, spec) // флаг спектатора (итер. 22)
	return dst, nil
}
