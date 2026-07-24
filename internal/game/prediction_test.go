package game

import (
	"testing"

	"arena/internal/protocol"
)

// predict сворачивает общий Step по ленте вводов от базового состояния. Это ровно
// то, что делает клиент при реконсиляции: берёт авторитетную позицию и
// переигрывает поверх неё неподтверждённые вводы. Здесь оно живёт в тесте — на
// проде это цикл в web/game.js, зеркалящий game.Step 1:1.
func predict(base MoveState, inputs []protocol.Input, dt float32) MoveState {
	s := base
	for _, in := range inputs {
		Step(&s, in, dt)
	}
	return s
}

// dropAcked отбрасывает вводы с seq <= ack — подтверждённые сервером. Оставшийся
// хвост клиент переигрывает поверх авторитетной позиции.
func dropAcked(tape []protocol.Input, ack uint32) []protocol.Input {
	i := 0
	for i < len(tape) && tape[i].Seq <= ack {
		i++
	}
	return tape[i:]
}

// inputTape строит детерминированную ленту вводов, перебирающую все направления
// (включая диагонали и упор в стены), с seq начиная с 1.
func inputTape(n int) []protocol.Input {
	dirs := []uint8{
		protocol.BtnRight,
		protocol.BtnRight | protocol.BtnDown,
		protocol.BtnDown,
		protocol.BtnDown | protocol.BtnLeft,
		protocol.BtnLeft,
		protocol.BtnLeft | protocol.BtnUp,
		protocol.BtnUp,
		protocol.BtnUp | protocol.BtnRight,
	}
	tape := make([]protocol.Input, 0, n)
	for i := 0; i < n; i++ {
		tape = append(tape, protocol.Input{Seq: uint32(i + 1), Buttons: dirs[i%len(dirs)]})
	}
	return tape
}

// authoritative прогоняет ленту через настоящий мир, один ввод на тик, и
// возвращает спавн, состояние после первых k вводов и состояние после всей ленты.
// Тот же seed → тот же спавн, поэтому клиент может воспроизвести старт.
func authoritative(t *testing.T, tape []protocol.Input, k int) (spawn, afterK, full MoveState) {
	t.Helper()
	const tickDt = 1.0 / 30 // длительность тика; движение интегрирует вводы шагом inputDt
	w := NewWorld(1)
	p, err := w.AddPlayer("pred")
	if err != nil {
		t.Fatalf("add player: %v", err)
	}
	spawn = p.MoveState
	// Один ввод на тик — как клиент шлёт их по одному; сервер осушает очередь.
	for i, in := range tape {
		w.EnqueueInput(p.ID, in)
		w.Step(tickDt)
		if i+1 == k {
			afterK = p.MoveState
		}
	}
	if k == 0 {
		afterK = spawn
	}
	full = p.MoveState
	if want := tape[len(tape)-1].Seq; p.LastProcessedSeq != want {
		t.Fatalf("LastProcessedSeq=%d, want %d", p.LastProcessedSeq, want)
	}
	return spawn, afterK, full
}

// TestPredictionMatchesAuthoritative — предсказание без реконсиляции: клиент,
// свернувший ту же ленту от того же спавна тем же общим Step, обязан прийти
// байт-в-байт туда же, куда пришёл авторитетный мир. Это фундамент предсказания:
// без него любая реконсиляция дрейфит.
func TestPredictionMatchesAuthoritative(t *testing.T) {
	tape := inputTape(200)
	spawn, _, full := authoritative(t, tape, 0)

	// Клиент сворачивает ту же ленту тем же общим Step шагом inputDt.
	if pred := predict(spawn, tape, inputDt); pred != full {
		t.Fatalf("prediction diverged from authoritative:\n pred=%+v\n auth=%+v", pred, full)
	}
}

// TestReconciliationMatchesAuthoritative — ядро итерации 4. Для каждого возможного
// частичного подтверждения k клиент:
//  1. берёт авторитетную позицию после первых k вводов (то, что пришло в снапшоте),
//  2. отбрасывает вводы с seq <= подтверждённого,
//  3. переигрывает остаток поверх авторитетной позиции —
//
// и обязан прийти ровно туда же, куда придёт сервер, обработав всю ленту. То есть
// реконсиляция не двигает итог: она лишь заменяет предсказанную базу на
// авторитетную, не теряя неподтверждённых вводов.
func TestReconciliationMatchesAuthoritative(t *testing.T) {
	tape := inputTape(200)

	for k := 0; k <= len(tape); k++ {
		_, afterK, full := authoritative(t, tape, k)

		ack := uint32(0)
		if k > 0 {
			ack = tape[k-1].Seq
		}
		pending := dropAcked(tape, ack)
		if recon := predict(afterK, pending, inputDt); recon != full {
			t.Fatalf("reconcile at k=%d (ack=%d) diverged:\n recon=%+v\n auth=%+v", k, ack, recon, full)
		}
	}
}

// TestStaleAckKeepsPending проверяет, что устаревшее (переупорядоченное)
// подтверждение не выбрасывает ещё не подтверждённые вводы: клиент отбрасывает
// строго по seq <= ack, а сервер держит LastProcessedSeq монотонным.
func TestStaleAckKeepsPending(t *testing.T) {
	tape := inputTape(10)
	// ack=3 подтверждает первые три; остаются seq 4..10.
	pending := dropAcked(tape, 3)
	if len(pending) != 7 || pending[0].Seq != 4 {
		t.Fatalf("dropAcked(3): got %d inputs starting at seq %d, want 7 starting at 4", len(pending), pending[0].Seq)
	}
	// Устаревший ack=1 (меньше уже подтверждённого) не должен «воскрешать» вводы:
	// клиент применяет его к уже подрезанному хвосту и ничего не теряет.
	if again := dropAcked(pending, 1); len(again) != len(pending) {
		t.Fatalf("stale ack changed pending: %d -> %d", len(pending), len(again))
	}
}

// TestStepDrainsAllQueuedInputs фиксирует серверную модель итерации 4: за один тик
// применяются ВСЕ накопившиеся вводы (клиент 60 Гц / тик 30 Гц ≈ 2 на тик), а не
// только последний. Клиент переигрывает каждый ввод — если бы сервер применял лишь
// последний, предсказание дрейфило бы на сменах направления.
func TestStepDrainsAllQueuedInputs(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("drain")
	if err != nil {
		t.Fatal(err)
	}
	start := p.MoveState

	// Два ввода вправо в одном тике должны сдвинуть на два шага inputDt, не на один.
	w.EnqueueInput(p.ID, protocol.Input{Seq: 1, Buttons: protocol.BtnRight})
	w.EnqueueInput(p.ID, protocol.Input{Seq: 2, Buttons: protocol.BtnRight})
	w.Step(1.0 / 30)

	want := predict(start, []protocol.Input{{Buttons: protocol.BtnRight}, {Buttons: protocol.BtnRight}}, inputDt)
	if p.MoveState != want {
		t.Fatalf("Step applied wrong number of inputs: got %+v want %+v", p.MoveState, want)
	}
	if p.LastProcessedSeq != 2 {
		t.Fatalf("LastProcessedSeq=%d, want 2", p.LastProcessedSeq)
	}
	if len(p.inputs) != 0 {
		t.Fatalf("input queue not drained: %d left", len(p.inputs))
	}
}
