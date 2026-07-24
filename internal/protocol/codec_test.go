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
		[]byte("not json"),
		[]byte(`{"t":1}`),                 // нет полезной нагрузки
		[]byte(`{"t":1,"d":{"s":"str"}}`), // неверный тип поля
		[]byte(`{"t":255,"d":{}}`),        // неизвестный тип
		[]byte(`{"t":2,"d":{"n":"`),       // обрезано
		[]byte(`{"t":2,"d":{"n":"seventeen_chars_x"}}`),        // имя = 17 байт > 16
		[]byte(`{"t":2,"d":{"n":"way_too_long_player_name"}}`), // имя > 16 байт
	}
	for i, data := range cases {
		if _, err := DecodeClient(data); err == nil {
			t.Errorf("case %d %q: expected error, got nil", i, data)
		}
	}
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
