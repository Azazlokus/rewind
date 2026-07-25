package protocol

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update перезаписывает golden-файлы вместо сравнения с ними. Запусти
// `go test ./internal/protocol -update` после осознанной смены формата и
// просмотри дифф перед коммитом.
var update = flag.Bool("update", false, "update golden files in testdata/")

// goldenCases фиксирует байты представительных сообщений на проводе. Если кодек
// случайно меняет формат — они падают; осознанное изменение фиксируется
// повторным запуском с -update. Итерация 3 сохраняет harness и перегенерирует
// файлы под бинарный формат.
func goldenCases(t *testing.T) map[string][]byte {
	t.Helper()
	snap := Snapshot{
		Tick:             7,
		LastProcessedSeq: 3,
		Entities: []Entity{
			{ID: 1, Kind: KindPlayer, X: 64, Y: 128, VX: 0, VY: -300, HP: 100},
		},
	}
	snapBytes, err := AppendSnapshot(nil, &snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	inputBytes, err := AppendInput(nil, Input{Seq: 5, Buttons: BtnUp | BtnRight, Aim: 16384})
	if err != nil {
		t.Fatalf("encode input: %v", err)
	}
	ackBytes, err := AppendJoinAck(nil, JoinAck{YourID: 2, Tick: 7})
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	spawnBytes, err := AppendSpawn(nil, Spawn{ID: 4, X: 64, Y: 128})
	if err != nil {
		t.Fatalf("encode spawn: %v", err)
	}
	deathBytes, err := AppendDeath(nil, Death{Victim: 4, Killer: 2})
	if err != nil {
		t.Fatalf("encode death: %v", err)
	}
	hitBytes, err := AppendHit(nil, Hit{Attacker: 2, Victim: 4, Damage: 25, VictimHP: 75})
	if err != nil {
		t.Fatalf("encode hit: %v", err)
	}
	return map[string][]byte{
		"snapshot.golden": snapBytes,
		"input.golden":    inputBytes,
		"joinack.golden":  ackBytes,
		"spawn.golden":    spawnBytes,
		"death.golden":    deathBytes,
		"hit.golden":      hitBytes,
	}
}

func TestGolden(t *testing.T) {
	for name, got := range goldenCases(t) {
		path := filepath.Join("testdata", name)
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("updated %s", path)
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s (run with -update to create): %v", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s mismatch:\n got %s\nwant %s", name, got, want)
		}
	}
}
