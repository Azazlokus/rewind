package game

import (
	"testing"
	"time"

	"arena/internal/protocol"
)

// TestReplayReconstructsWorld: комната записывает сессию (join/leave/input со
// штампом тика), лог гоняется через кодек, реплей проигрывает его headless — и
// Checksum итогового мира совпадает с записанным байт-в-байт. Это фундамент
// реплеев и регрессий на desync.
func TestReplayReconstructsWorld(t *testing.T) {
	const tickRate = 30
	dt := tickDt(tickRate)

	w := NewWorld(1)
	w.EnableReplayRecording()
	a, err := w.AddPlayer("alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.AddPlayer("bob")
	if err != nil {
		t.Fatal(err)
	}

	var seq uint32
	for tk := 0; tk < 50; tk++ {
		seq++
		// alice едет вправо и раз в 5 тиков стреляет; bob едет вверх.
		aliceBtn := protocol.BtnRight
		if tk%5 == 0 {
			aliceBtn |= protocol.BtnFire
		}
		w.EnqueueInput(a.ID, protocol.Input{Seq: seq, Buttons: aliceBtn, Aim: protocol.AimFromRadians(0)})
		w.EnqueueInput(b.ID, protocol.Input{Seq: seq, Buttons: protocol.BtnUp})
		if tk == 30 {
			w.RemovePlayer(b.ID) // bob уходит посреди сессии
		}
		w.Step(dt)
	}
	want := w.Checksum()

	log := w.ReplayLog(tickRate)
	if log == nil {
		t.Fatal("recording not enabled")
	}
	if log.Ticks != 50 {
		t.Fatalf("log Ticks=%d, want 50", log.Ticks)
	}

	// Лог проходит через кодек (round-trip), затем проигрывается.
	decoded, err := DecodeReplay(log.Encode())
	if err != nil {
		t.Fatalf("DecodeReplay: %v", err)
	}
	if decoded.Seed != 1 || decoded.TickRate != tickRate || decoded.Ticks != 50 {
		t.Fatalf("decoded header off: seed=%d tickRate=%d ticks=%d", decoded.Seed, decoded.TickRate, decoded.Ticks)
	}
	if decoded.Len() != log.Len() {
		t.Fatalf("decoded %d events, want %d", decoded.Len(), log.Len())
	}

	got, err := Replay(decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got != want {
		t.Fatalf("replay checksum %#x != original %#x", got, want)
	}
}

// TestReplayDashRoundTrip: рывок (Input.Actions, итер. 27) переживает запись→кодек→
// реплей. Рывок влияет на движение (в Checksum), поэтому лог обязан хранить Actions
// (байт v3); до фикса он терялся и реплей рассинхронивался. Негативный контроль:
// затираем Actions в декодированном логе — Checksum обязан разойтись.
func TestReplayDashRoundTrip(t *testing.T) {
	const tickRate = 30
	dt := tickDt(tickRate)
	w := NewWorld(5)
	w.EnableReplayRecording()
	p, err := w.AddPlayer("dasher")
	if err != nil {
		t.Fatal(err)
	}
	for tk := 0; tk < 40; tk++ {
		in := protocol.Input{Seq: uint32(tk + 1), Buttons: protocol.BtnRight}
		if tk == 2 || tk == 20 { // дважды рвём — оба должны воспроизвестись
			in.Actions = protocol.ActDash
		}
		w.EnqueueInput(p.ID, in)
		w.Step(dt)
	}
	want := w.Checksum()

	decoded, err := DecodeReplay(w.ReplayLog(tickRate).Encode())
	if err != nil {
		t.Fatalf("DecodeReplay: %v", err)
	}
	got, err := Replay(decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got != want {
		t.Fatalf("replay checksum %#x != original %#x — dash Actions lost in log", got, want)
	}

	// Негативный контроль: без Actions реплей обязан разойтись (иначе тест ничего не ловит).
	stripped, err := DecodeReplay(w.ReplayLog(tickRate).Encode())
	if err != nil {
		t.Fatal(err)
	}
	for i := range stripped.events {
		stripped.events[i].in.Actions = 0
	}
	if h, _ := Replay(stripped); h == want {
		t.Fatal("stripping Actions did not change checksum — test is not exercising dash")
	}
}

// TestReplayDeterministicAcrossRuns: один и тот же лог, проигранный дважды, даёт
// один и тот же хэш (реплей сам детерминирован).
func TestReplayDeterministicAcrossRuns(t *testing.T) {
	w := NewWorld(7)
	w.EnableReplayRecording()
	p, _ := w.AddPlayer("solo")
	for tk := 0; tk < 20; tk++ {
		w.EnqueueInput(p.ID, protocol.Input{Seq: uint32(tk + 1), Buttons: protocol.BtnRight | protocol.BtnFire})
		w.Step(tickDt(30))
	}
	log := w.ReplayLog(30)

	h1, err := Replay(log)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Replay(log)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("replay not deterministic: %#x != %#x", h1, h2)
	}
}

// TestDecodeReplayRejectsGarbage: декодер лога не паникует и возвращает ошибку на
// мусоре/обрезках (фаззится в 7B, здесь — базовые случаи).
func TestDecodeReplayRejectsGarbage(t *testing.T) {
	good := (&ReplayLog{Seed: 1, TickRate: 30, Ticks: 1}).Encode()
	cases := [][]byte{
		nil,
		{},
		{'A', 'R', 'P', 'L'},                // только magic, заголовок обрезан
		append([]byte("XXXX"), good[4:]...), // битый magic
		good[:len(good)-1],                  // обрезан на последнем байте заголовка (Ticks=1, но событий нет — count=0, ок; обрежем сильнее)
	}
	// Лог с заявленным событием, но без тела. eventCount в v5 (итер. 30) — по смещению
	// 24 (после байтов teamMode@21, hillMode@22 и domMode@23).
	claim := (&ReplayLog{Seed: 1, TickRate: 30}).Encode()
	claim[24] = 1 // eventCount = 1, а тела нет
	cases = append(cases, claim)
	// tickRate = 0 сломал бы dt делением на ноль (крэшер, пойманный фаззером).
	cases = append(cases, (&ReplayLog{Seed: 1, TickRate: 0, Ticks: 1}).Encode())

	for i, data := range cases {
		if _, err := DecodeReplay(data); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

// TestRoomReplayMatchesLive: комната пишет лог реплея через реальный путь
// inbox→handle→World; после остановки реплей лога воспроизводит итоговое состояние
// мира байт-в-байт (тот же Checksum). Так проверяется, что штампы тика Room
// выставляет верно.
func TestRoomReplayMatchesLive(t *testing.T) {
	tr := newTestRoom(t, Config{Seed: 3, RecordReplay: true})
	a := tr.join("alice")
	b := tr.join("bob")

	var seq uint32
	for range 30 {
		seq++
		a.sendInput(protocol.Input{Seq: seq, Buttons: protocol.BtnRight | protocol.BtnFire, Aim: protocol.AimFromRadians(0)})
		b.sendInput(protocol.Input{Seq: seq, Buttons: protocol.BtnLeft})
		tr.tick(1)
		a.read() // читаем оба, иначе отставших выкинут
		b.read()
	}

	// Останавливаем комнату: после Done() мир и лог безопасны для чтения из теста
	// (shutdown убирает игроков — итоговый мир пуст, но с тем же тиком и курсором rng).
	tr.cancel()
	select {
	case <-tr.room.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("room did not stop")
	}

	live := tr.room.world.Checksum()
	log := tr.room.ReplayLog()
	if log == nil {
		t.Fatal("recording not enabled")
	}
	got, err := Replay(log)
	if err != nil {
		t.Fatal(err)
	}
	if got != live {
		t.Fatalf("replay checksum %#x != live room %#x", got, live)
	}
}

// TestReplayRecordsOnlyAcceptedInputs: в лог идут лишь вводы, прошедшие дедуп в
// EnqueueInput; дропнутые повторы/реордер не пишутся, а реплей всё равно
// воспроизводит гейт дедупа (lastQueuedSeq/hasQueued в Checksum). Стережёт
// утверждение, которое иначе держалось бы только на комментарии.
func TestReplayRecordsOnlyAcceptedInputs(t *testing.T) {
	dt := tickDt(30)
	w := NewWorld(1)
	w.EnableReplayRecording()
	p, err := w.AddPlayer("dup")
	if err != nil {
		t.Fatal(err)
	}

	w.EnqueueInput(p.ID, protocol.Input{Seq: 10, Buttons: protocol.BtnRight}) // принят
	w.Step(dt)
	w.EnqueueInput(p.ID, protocol.Input{Seq: 5, Buttons: protocol.BtnLeft})  // дроп: seq < lastQueuedSeq
	w.EnqueueInput(p.ID, protocol.Input{Seq: 10, Buttons: protocol.BtnUp})   // дроп: seq == lastQueuedSeq
	w.EnqueueInput(p.ID, protocol.Input{Seq: 12, Buttons: protocol.BtnDown}) // принят
	w.Step(dt)
	want := w.Checksum()

	log := w.ReplayLog(30)
	// 1 join + 2 принятых ввода; дропнутые два — не в логе.
	if log.Len() != 3 {
		t.Fatalf("log has %d events, want 3 (1 join + 2 accepted inputs)", log.Len())
	}

	decoded, err := DecodeReplay(log.Encode())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Replay(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("replay %#x != live %#x — гейт дедупа (lastQueuedSeq/hasQueued) не воспроизведён", got, want)
	}
}

// FuzzReplayDecode: декодер лога не паникует на любом вводе; успешный decode
// переживает обратное кодирование, а Replay (при вменяемом числе тиков) не паникует.
func FuzzReplayDecode(f *testing.F) {
	f.Add((&ReplayLog{Seed: 1, TickRate: 30, Ticks: 2}).Encode())
	rich := &ReplayLog{Seed: 5, TickRate: 30, Ticks: 2}
	rich.join(0, "p")
	rich.input(0, 1, protocol.Input{Seq: 1, Buttons: protocol.BtnRight})
	rich.leave(1, 1)
	f.Add(rich.Encode())
	f.Add([]byte{'A', 'R', 'P', 'L', 1})
	f.Add([]byte(nil))
	f.Add((&ReplayLog{Seed: 1, TickRate: 0, Ticks: 1}).Encode()) // крэшер: tickRate=0

	f.Fuzz(func(t *testing.T, data []byte) {
		log, err := DecodeReplay(data)
		if err != nil {
			return
		}
		_ = log.Encode() // успешный decode обязан пережить re-encode
		// Replay не должен паниковать; ограничиваем число тиков, чтобы фаззер не
		// зациклился на огромном Ticks с пустыми Step.
		if log.Ticks <= 10000 {
			_, _ = Replay(log)
		}
	})
}
