package protocol

import "testing"

// FuzzDecode утверждает единственный жёсткий инвариант обоих декодеров
// (DecodeClient и DecodeServer): они никогда не паникуют, каким бы кривым ни был
// ввод. Падение здесь — это баг типа отказ-в-обслуживании, поэтому этот фаззер
// защищает весь путь приёма. Плюс проверяет, что успешно декодированное сообщение
// сериализуется обратно без ошибки.
func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		nil,
		{byte(MsgInput), 1, 0, 0, 0, 2, 3, 0, 9, 0, 0, 0, 5, 0, 0, 0}, // валидный Input (viewTick+ackTick)
		{byte(MsgJoin), 6, 'p', 'l', 'a', 'y', 'e', 'r'},              // валидный Join
		{byte(MsgJoin), 17},                 // имя слишком длинное
		{byte(MsgJoin), 5, 'a'},             // обрезанное имя
		{0xff, 0x01, 0x02, 0x03},            // неизвестный тип + мусор
		{byte(MsgInput)},                    // обрезанный Input
		{byte(MsgSpawn), 3, 0, 8, 6, 4, 12}, // валидный Spawn
		{byte(MsgDeath), 5, 0, 9, 0},        // валидный Death
		{byte(MsgHit), 2, 0, 5, 0, 25, 75},  // валидный Hit
		// полный снапшот без сущностей: tick=7, baseTick=0, ls=3, count=0, removed=0
		{byte(MsgSnapshot), 7, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0},
		// дельта: tick=8, baseTick=7, ls=3, count=0, removed=1 [id=2]
		{byte(MsgSnapshot), 8, 0, 0, 0, 7, 0, 0, 0, 3, 0, 0, 0, 0, 1, 2, 0},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Ни один декодер не должен паниковать; успешный decode обязан пережить
		// обратное кодирование без ошибки.
		if msg, err := DecodeClient(data); err == nil {
			switch msg.Type {
			case MsgInput:
				if _, err := AppendInput(nil, msg.Input); err != nil {
					t.Fatalf("re-encode input %+v: %v", msg.Input, err)
				}
			case MsgJoin:
				if _, err := AppendJoin(nil, msg.Join); err != nil {
					t.Fatalf("re-encode join %+v: %v", msg.Join, err)
				}
			default:
				t.Fatalf("DecodeClient returned unexpected type %v from %q", msg.Type, data)
			}
		}

		var out ServerMessage
		if err := DecodeServer(data, &out); err == nil {
			var e error
			switch out.Type {
			case MsgSnapshot:
				_, e = AppendSnapshot(nil, &out.Snapshot)
			case MsgJoinAck:
				_, e = AppendJoinAck(nil, out.JoinAck)
			case MsgSpawn:
				_, e = AppendSpawn(nil, out.Spawn)
			case MsgDeath:
				_, e = AppendDeath(nil, out.Death)
			case MsgHit:
				_, e = AppendHit(nil, out.Hit)
			default:
				t.Fatalf("DecodeServer returned unexpected type %v from %q", out.Type, data)
			}
			if e != nil {
				t.Fatalf("re-encode server %v: %v", out.Type, e)
			}
		}
	})
}
