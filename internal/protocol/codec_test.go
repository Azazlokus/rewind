package protocol

import (
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

// TestClientRoundTrip кодирует, затем декодирует каждое клиентское сообщение и
// проверяет совпадение. Бинарный кодек итерации 3 обязан продолжать проходить
// этот тест без изменений: формат может меняться, гарантия round-trip — нет.
func TestClientRoundTrip(t *testing.T) {
	inputs := []Input{
		{Seq: 0, Buttons: 0, Aim: 0},
		{Seq: 1, Buttons: BtnUp | BtnFire, Aim: 12345},
		{Seq: math.MaxUint32, Buttons: 0xff, Aim: math.MaxUint16},
	}
	for _, in := range inputs {
		buf, err := AppendInput(nil, in)
		if err != nil {
			t.Fatalf("AppendInput(%+v): %v", in, err)
		}
		got, err := DecodeClient(buf)
		if err != nil {
			t.Fatalf("DecodeClient: %v", err)
		}
		if got.Type != MsgInput || got.Input != in {
			t.Fatalf("input round-trip: got %+v want type=%v %+v", got, MsgInput, in)
		}
	}

	joins := []Join{{Name: ""}, {Name: "player"}, {Name: "sixteen_chars_ok"}}
	for _, j := range joins {
		buf, err := AppendJoin(nil, j)
		if err != nil {
			t.Fatalf("AppendJoin(%q): %v", j.Name, err)
		}
		got, err := DecodeClient(buf)
		if err != nil {
			t.Fatalf("DecodeClient: %v", err)
		}
		if got.Type != MsgJoin || got.Join != j {
			t.Fatalf("join round-trip: got %+v want %+v", got.Join, j)
		}
	}
}

// TestServerRoundTrip покрывает сообщения сервер -> клиент.
func TestServerRoundTrip(t *testing.T) {
	snap := Snapshot{
		Tick:             42,
		LastProcessedSeq: 7,
		Entities: []Entity{
			{ID: 1, Kind: KindPlayer, X: 100.5, Y: 200.25, VX: -10, VY: 5, HP: 100},
			{ID: 2, Kind: KindProjectile, X: 0, Y: 4095, VX: 300, VY: 0, HP: 1},
		},
	}
	buf, err := AppendSnapshot(nil, &snap)
	if err != nil {
		t.Fatalf("AppendSnapshot: %v", err)
	}
	var out ServerMessage
	if err := DecodeServer(buf, &out); err != nil {
		t.Fatalf("DecodeServer: %v", err)
	}
	if out.Type != MsgSnapshot {
		t.Fatalf("type: got %v want Snapshot", out.Type)
	}
	if !reflect.DeepEqual(out.Snapshot, snap) {
		t.Fatalf("snapshot round-trip:\n got %+v\nwant %+v", out.Snapshot, snap)
	}

	ack := JoinAck{YourID: 9, Tick: 1000}
	buf, err = AppendJoinAck(nil, ack)
	if err != nil {
		t.Fatalf("AppendJoinAck: %v", err)
	}
	out = ServerMessage{}
	if err := DecodeServer(buf, &out); err != nil {
		t.Fatalf("DecodeServer ack: %v", err)
	}
	if out.Type != MsgJoinAck || out.JoinAck != ack {
		t.Fatalf("ack round-trip: got %+v want %+v", out.JoinAck, ack)
	}

	// Reliable-события итерации 5. Координаты Spawn берём на сетке 1/16, чтобы
	// квантование было точным и сравнение через == прошло.
	spawn := Spawn{ID: 3, X: 100.5, Y: 200.25}
	buf, err = AppendSpawn(nil, spawn)
	if err != nil {
		t.Fatalf("AppendSpawn: %v", err)
	}
	out = ServerMessage{}
	if err := DecodeServer(buf, &out); err != nil {
		t.Fatalf("DecodeServer spawn: %v", err)
	}
	if out.Type != MsgSpawn || out.Spawn != spawn {
		t.Fatalf("spawn round-trip: got %+v want %+v", out.Spawn, spawn)
	}

	death := Death{Victim: 5, Killer: 9}
	buf, err = AppendDeath(nil, death)
	if err != nil {
		t.Fatalf("AppendDeath: %v", err)
	}
	out = ServerMessage{}
	if err := DecodeServer(buf, &out); err != nil {
		t.Fatalf("DecodeServer death: %v", err)
	}
	if out.Type != MsgDeath || out.Death != death {
		t.Fatalf("death round-trip: got %+v want %+v", out.Death, death)
	}

	hit := Hit{Attacker: 2, Victim: 5, Damage: 25, VictimHP: 75}
	buf, err = AppendHit(nil, hit)
	if err != nil {
		t.Fatalf("AppendHit: %v", err)
	}
	out = ServerMessage{}
	if err := DecodeServer(buf, &out); err != nil {
		t.Fatalf("DecodeServer hit: %v", err)
	}
	if out.Type != MsgHit || out.Hit != hit {
		t.Fatalf("hit round-trip: got %+v want %+v", out.Hit, hit)
	}
}

// TestPropertyRoundTrip прогоняет случайные вводы сквозь кодек — свойство, на
// котором держится весь протокол.
func TestPropertyRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		in := Input{Seq: r.Uint32(), Buttons: uint8(r.UintN(256)), Aim: uint16(r.UintN(65536))}
		buf, err := AppendInput(nil, in)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := DecodeClient(buf)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Input != in {
			t.Fatalf("mismatch: got %+v want %+v", got.Input, in)
		}
	}
}

// TestDecodeRejectsGarbage проверяет, что декодер возвращает ошибку, а не панику,
// на пустом, обрезанном вводе и вводе с неизвестным типом.
func TestDecodeRejectsGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0xff},                         // неизвестный тип
		{byte(MsgInput)},               // Input без тела
		{byte(MsgInput), 0, 0, 0},      // Input: тело обрезано (нужно 7 байт)
		{byte(MsgJoin)},                // Join без байта длины
		{byte(MsgJoin), 17},            // Join: длина имени 17 > 16
		{byte(MsgJoin), 5, 'a'},        // Join: имя обрезано (заявлено 5, дан 1)
		{byte(MsgJoin), 2, 0xff, 0xfe}, // Join: имя не UTF-8
		{byte(MsgSnapshot), 0, 0, 0},   // Snapshot: заголовок обрезан
		snapshotHeader(7, 3, 5),        // Snapshot: count=5, но сущностей нет
	}
	for i, data := range cases {
		if _, err := DecodeClient(data); err == nil {
			// MsgSnapshot — серверное сообщение, для клиента это неизвестный тип,
			// тоже ошибка, что и требуется.
			t.Errorf("DecodeClient case %d %v: expected error, got nil", i, data)
		}
	}
	// Отдельно прогоняем серверный декодер по обрезанным серверным сообщениям.
	var out ServerMessage
	serverCases := [][]byte{
		nil,
		{0xff},
		{byte(MsgSnapshot), 0, 0, 0},
		snapshotHeader(7, 3, 5),       // count=5, тела нет
		{byte(MsgJoinAck), 1, 0},      // JoinAck: тело обрезано (нужно 6)
		{byte(MsgSpawn), 0, 0, 0},     // Spawn: тело обрезано (нужно 6)
		{byte(MsgDeath), 1, 0},        // Death: тело обрезано (нужно 4)
		{byte(MsgHit), 1, 0, 2, 0, 3}, // Hit: тело обрезано (нужно 6, дано 5)
	}
	for i, data := range serverCases {
		if err := DecodeServer(data, &out); err == nil {
			t.Errorf("DecodeServer case %d %v: expected error, got nil", i, data)
		}
	}
}

// snapshotHeader строит заголовок снапшота без тела сущностей.
func snapshotHeader(tick, lastSeq uint32, count byte) []byte {
	b := []byte{byte(MsgSnapshot)}
	b = append(b, byte(tick), byte(tick>>8), byte(tick>>16), byte(tick>>24))
	b = append(b, byte(lastSeq), byte(lastSeq>>8), byte(lastSeq>>16), byte(lastSeq>>24))
	return append(b, count)
}

// TestSnapshotQuantization проверяет, что квантование позиций и скоростей
// возвращает значения в пределах шага 1/CoordScale.
func TestSnapshotQuantization(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	const tol = 1.0/CoordScale/2 + 1e-4 // половина шага + запас на float32
	for range 500 {
		in := Snapshot{
			Tick: r.Uint32(),
			Entities: []Entity{{
				ID:   uint16(r.UintN(65536)),
				Kind: KindPlayer,
				X:    r.Float32() * MapSize,
				Y:    r.Float32() * MapSize,
				VX:   (r.Float32()*2 - 1) * MaxSpeed,
				VY:   (r.Float32()*2 - 1) * MaxSpeed,
				HP:   uint8(r.UintN(256)),
			}},
		}
		buf, err := AppendSnapshot(nil, &in)
		if err != nil {
			t.Fatal(err)
		}
		var out ServerMessage
		if err := DecodeServer(buf, &out); err != nil {
			t.Fatal(err)
		}
		g := out.Snapshot.Entities[0]
		w := in.Entities[0]
		if g.ID != w.ID || g.Kind != w.Kind || g.HP != w.HP {
			t.Fatalf("non-quantized field mismatch: got %+v want %+v", g, w)
		}
		if abs32(g.X-w.X) > tol || abs32(g.Y-w.Y) > tol {
			t.Fatalf("coord off: got (%.4f,%.4f) want (%.4f,%.4f)", g.X, g.Y, w.X, w.Y)
		}
		if abs32(g.VX-w.VX) > tol || abs32(g.VY-w.VY) > tol {
			t.Fatalf("vel off: got (%.4f,%.4f) want (%.4f,%.4f)", g.VX, g.VY, w.VX, w.VY)
		}
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func TestNameLengthEnforced(t *testing.T) {
	if _, err := AppendJoin(nil, Join{Name: "this name is too long"}); err == nil {
		t.Fatal("expected AppendJoin to reject an over-length name")
	}
}

func TestAimQuantisation(t *testing.T) {
	for _, deg := range []float64{0, 90, 180, 270, 359} {
		rad := deg * math.Pi / 180
		q := AimFromRadians(rad)
		back := float64(Input{Aim: q}.AimRadians())
		diff := math.Abs(back - rad)
		if diff > 2*math.Pi/65536+1e-6 {
			t.Errorf("deg=%.0f: round-trip off by %.6f rad", deg, diff)
		}
	}
}
