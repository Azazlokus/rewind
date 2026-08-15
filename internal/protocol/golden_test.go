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
	// Дельта-снапшот (field-level, итерация 9): база tick 5, одна изменённая сущность
	// (сдвинулась — X,Y) и один ушедший id. Фиксирует раскладку дельты с маской полей
	// на проводе: [2B id][1B маска][2B x][2B y] вместо полной 12-байтной записи.
	deltaSnap := Snapshot{
		Tick:             7,
		BaseTick:         5,
		LastProcessedSeq: 3,
		Entities:         []Entity{{ID: 1, X: 64, Y: 128}},
		Masks:            []uint8{FieldX | FieldY},
		Removed:          []uint16{2},
	}
	deltaBytes, err := AppendSnapshot(nil, &deltaSnap)
	if err != nil {
		t.Fatalf("encode delta snapshot: %v", err)
	}
	inputBytes, err := AppendInput(nil, Input{Seq: 5, Buttons: BtnUp | BtnRight, Aim: 16384, ViewTick: 6})
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
	// PickupState (итерация 19): две активные точки — фиксирует раскладку
	// [1B type][1B count] count × [1B spot][1B kind] на проводе.
	pickupBytes, err := AppendPickupState(nil, PickupState{
		Active: []Pickup{{Spot: 0, Kind: 1}, {Spot: 4, Kind: 3}},
	})
	if err != nil {
		t.Fatalf("encode pickupstate: %v", err)
	}
	// Killstreak (итерация 20): фиксирует раскладку [type][2B id][2B streak].
	killstreakBytes, err := AppendKillstreak(nil, Killstreak{ID: 7, Streak: 6})
	if err != nil {
		t.Fatalf("encode killstreak: %v", err)
	}
	// WeaponState (итер. 26): фиксирует [type][1B count] count × [2B id][1B weapon].
	weaponBytes, err := AppendWeaponState(nil, WeaponState{
		Weapons: []WeaponInfo{{ID: 1, Weapon: 2}, {ID: 5, Weapon: 4}},
	})
	if err != nil {
		t.Fatalf("encode weaponstate: %v", err)
	}
	// FlagState (итер. 31): фиксирует [type][1B count] count ×
	// [1B team][1B status][2B carrier][2B x][2B y] (позиции квантованы).
	flagBytes, err := AppendFlagState(nil, FlagState{
		Flags: []FlagInfo{{Team: 0, Status: 0, Carrier: 0, X: 512, Y: 2048}, {Team: 1, Status: 1, Carrier: 7, X: 1600, Y: 2000}},
	})
	if err != nil {
		t.Fatalf("encode flagstate: %v", err)
	}
	// Capture (итер. 31): фиксирует [type][2B playerID][1B team].
	captureBytes, err := AppendCapture(nil, Capture{Player: 9, Team: 1})
	if err != nil {
		t.Fatalf("encode capture: %v", err)
	}
	return map[string][]byte{
		"pickupstate.golden":    pickupBytes,
		"killstreak.golden":     killstreakBytes,
		"weaponstate.golden":    weaponBytes,
		"flagstate.golden":      flagBytes,
		"capture.golden":        captureBytes,
		"snapshot.golden":       snapBytes,
		"snapshot_delta.golden": deltaBytes,
		"input.golden":          inputBytes,
		"joinack.golden":        ackBytes,
		"spawn.golden":          spawnBytes,
		"death.golden":          deathBytes,
		"hit.golden":            hitBytes,
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
