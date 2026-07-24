package protocol

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update rewrites the golden files instead of comparing against them. Run
// `go test ./internal/protocol -update` after an intentional format change, and
// review the diff before committing.
var update = flag.Bool("update", false, "update golden files in testdata/")

// goldenCases pins the on-wire bytes of representative messages. If the codec
// changes the format by accident, these fail; a deliberate change is recorded by
// re-running with -update. Iteration 3 keeps the harness and regenerates the
// files for the binary format.
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
	return map[string][]byte{
		"snapshot.golden": snapBytes,
		"input.golden":    inputBytes,
		"joinack.golden":  ackBytes,
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
