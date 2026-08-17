package main

import (
	"os"
	"testing"
)

// TestClientConstantsUpToDate ловит дрейф между Go-источником констант
// (internal/protocol + internal/game) и сгенерированным блоком в web/game.js.
// Прогоняется в make check: если кто-то поменял константу/бит/геометрию в Go, но не
// перегенерировал клиента, тест падает с инструкцией. Регенерация: `make gen`.
func TestClientConstantsUpToDate(t *testing.T) {
	path := targetPath()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	want, err := spliceBlock(string(src), render())
	if err != nil {
		t.Fatalf("splice web/game.js: %v", err)
	}
	if want != string(src) {
		t.Fatalf("web/game.js generated constants are out of date — run: make gen\n" +
			"(the mirror block between GENERATED-BEGIN/END no longer matches internal/protocol + internal/game)")
	}
}

// TestSpliceBlockRoundTrip проверяет, что spliceBlock заменяет ровно межмаркерную
// область и сохраняет всё вокруг.
func TestSpliceBlockRoundTrip(t *testing.T) {
	src := "head\n// GENERATED-BEGIN\nOLD\n// GENERATED-END\ntail\n"
	out, err := spliceBlock(src, "NEW\n")
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	want := "head\n// GENERATED-BEGIN\nNEW\n// GENERATED-END\ntail\n"
	if out != want {
		t.Fatalf("splice mismatch:\n got %q\nwant %q", out, want)
	}
}

// TestSpliceBlockMissingMarker: без маркеров — ошибка, а не тихая порча файла.
func TestSpliceBlockMissingMarker(t *testing.T) {
	if _, err := spliceBlock("no markers here", "X"); err == nil {
		t.Fatal("expected error when markers are absent")
	}
}
