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
		{byte(MsgInput), 1, 0, 0, 0, 2, 3, 0, 9, 0, 0, 0, 5, 0, 0, 0},    // валидный Input (viewTick+ackTick)
		{byte(MsgInput), 1, 0, 0, 0, 2, 3, 0, 9, 0, 0, 0, 5, 0, 0, 0, 1}, // Input с байтом actions (итер. 27)
		{byte(MsgJoin), 6, 'p', 'l', 'a', 'y', 'e', 'r', 0, 0},           // валидный Join без токена
		{byte(MsgJoin), 4, 'a', 'c', 'c', 't', 3, 0, 'a', '.', 'b'},      // Join с токеном
		{byte(MsgJoin), 5, 'w', 'a', 't', 'c', 'h', 0, 0, 1},             // Join спектатора (итер. 22)
		{byte(MsgJoin), 6, 'p', 'l', 'a', 'y', 'e', 'r'},                 // нет длины токена (обрезка)
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
		// дельта с сущностью (итерация 9): tick=8, baseTick=7, ls=3, count=1,
		// ent[id=1, mask=X|Y, x=64, y=128], removed=1 [id=2]
		{byte(MsgSnapshot), 8, 0, 0, 0, 7, 0, 0, 0, 3, 0, 0, 0, 1,
			1, 0, byte(FieldX | FieldY), 64, 0, 128, 0, 1, 2, 0},
		// MatchState (итер. 14 + команды 23 + холм 29): phase=1, remaining=300, winner=1,
		// flags=3 (teamMode|hillMode), count=1, score[id=1, kills=3, deaths=2, team=1,
		// hillScore=7, name="ab"]
		{byte(MsgMatchState), 1, 0x2c, 1, 0, 0, 1, 0, 3, 1, 1, 0, 3, 0, 2, 0, 1, 7, 0, 2, 'a', 'b'},
		// MatchState доминация (итер. 30): phase=0, remaining=100, winner=0, flags=4
		// (domMode), count=1, score[id=2, kills=1, deaths=0, team=0, objScore=9, name="z"]
		{byte(MsgMatchState), 0, 0x64, 0, 0, 0, 0, 0, 4, 1, 2, 0, 1, 0, 0, 0, 0, 9, 0, 1, 'z'},
		// PickupState (итерация 19): count=2, [spot=0,kind=1][spot=4,kind=3]
		{byte(MsgPickupState), 2, 0, 1, 4, 3},
		{byte(MsgPickupState), 3, 0, 1}, // count больше данных — декодер не паникует
		// Killstreak (итерация 20): id=7, streak=6
		{byte(MsgKillstreak), 7, 0, 6, 0},
		{byte(MsgKillstreak), 7, 0}, // обрезка — декодер не паникует
		// WeaponState (итер. 26): count=2, [id=1,weapon=2][id=5,weapon=4]
		{byte(MsgWeaponState), 2, 1, 0, 2, 5, 0, 4},
		{byte(MsgWeaponState), 3, 1, 0}, // count больше данных — декодер не паникует
		// FlagState (итер. 31): count=2, [team=0,status=0,carrier=0,x,y][team=1,status=1,carrier=7,x,y]
		{byte(MsgFlagState), 2, 0, 0, 0, 0, 10, 0, 20, 0, 1, 1, 7, 0, 30, 0, 40, 0},
		{byte(MsgFlagState), 2, 0, 0}, // count больше данных — декодер не паникует
		// Capture (итер. 31): player=9, team=1
		{byte(MsgCapture), 9, 0, 1},
		{byte(MsgCapture), 9}, // обрезка — декодер не паникует
		// MatchState CTF (итер. 31): flags=8 (ctfMode), count=1, score[id=4,captures=3,name="e"]
		{byte(MsgMatchState), 1, 0x90, 1, 0, 0, 1, 0, 8, 1, 4, 0, 2, 0, 3, 0, 0, 3, 0, 1, 'e'},
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
			case MsgMatchState:
				_, e = AppendMatchState(nil, out.MatchState)
			case MsgPickupState:
				_, e = AppendPickupState(nil, out.PickupState)
			case MsgKillstreak:
				_, e = AppendKillstreak(nil, out.Killstreak)
			case MsgWeaponState:
				_, e = AppendWeaponState(nil, out.WeaponState)
			case MsgFlagState:
				_, e = AppendFlagState(nil, out.FlagState)
			case MsgCapture:
				_, e = AppendCapture(nil, out.Capture)
			default:
				t.Fatalf("DecodeServer returned unexpected type %v from %q", out.Type, data)
			}
			if e != nil {
				t.Fatalf("re-encode server %v: %v", out.Type, e)
			}
		}
	})
}
