// Command genclient генерирует блок зеркалимых констант в web/game.js из
// internal/protocol и internal/game (итер. 41). Раньше клиент зеркалил провод и
// геометрию вручную — это дрейфило (напр. сломанный разбор MsgMatchState в итер. 23).
// Теперь источник истины — Go, а этот генератор переносит значения в клиент.
//
// Запуск: go run ./cmd/genclient  (или make gen). Блок в web/game.js ограничен
// маркерами GENERATED-BEGIN/END; всё вне них — ручной клиентский код и не трогается.
// Дрейф между Go и клиентом ловит genclient_test.go (в make check).
package main

//go:generate go run .

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"arena/internal/game"
	"arena/internal/protocol"
)

const (
	beginMarker = "// GENERATED-BEGIN"
	endMarker   = "// GENERATED-END"
)

func main() {
	path := targetPath()
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genclient: read %s: %v\n", path, err)
		os.Exit(1)
	}
	out, err := spliceBlock(string(src), render())
	if err != nil {
		fmt.Fprintf(os.Stderr, "genclient: %v\n", err)
		os.Exit(1)
	}
	if out == string(src) {
		fmt.Println("genclient: web/game.js already up to date")
		return
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "genclient: write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Println("genclient: regenerated web/game.js constants")
}

// targetPath возвращает путь к web/game.js независимо от cwd — по расположению этого
// исходника (repo/cmd/genclient/main.go → repo/web/game.js). Так и `go run`, и тест
// (у которого cwd — каталог пакета) находят один и тот же файл.
func targetPath() string {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		panic("genclient: cannot locate own source path")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(self)))
	return filepath.Join(root, "web", "game.js")
}

// spliceBlock заменяет содержимое между маркерами на block, сохраняя сами маркеры и
// всё вне них. Маркеры ожидаются каждый на своей строке (без отступа).
func spliceBlock(src, block string) (string, error) {
	bi := strings.Index(src, beginMarker)
	if bi < 0 {
		return "", fmt.Errorf("marker %q not found in web/game.js", beginMarker)
	}
	ei := strings.Index(src, endMarker)
	if ei < 0 {
		return "", fmt.Errorf("marker %q not found in web/game.js", endMarker)
	}
	if ei < bi {
		return "", fmt.Errorf("marker %q precedes %q", endMarker, beginMarker)
	}
	// Начало строки сразу после строки begin-маркера.
	afterBegin := bi + len(beginMarker)
	nl := strings.IndexByte(src[afterBegin:], '\n')
	if nl < 0 {
		return "", fmt.Errorf("begin marker line not terminated")
	}
	blockStart := afterBegin + nl + 1
	return src[:blockStart] + block + src[ei:], nil
}

// render строит JS-блок констант из поверхностей ClientWire/ClientSim. Вывод должен
// быть детерминированным и валидным JS: сравнивается байт-в-байт в тесте.
func render() string {
	w := protocol.ClientWire()
	s := game.ClientSim()
	var b strings.Builder

	kv := func(k, v string) { fmt.Fprintf(&b, "  %s: %s,\n", k, v) }

	b.WriteString("const PROTO = {\n")
	kv("MsgInput", hx(w.MsgInput))
	kv("MsgJoin", hx(w.MsgJoin))
	kv("MsgSnapshot", hx(w.MsgSnapshot))
	kv("MsgJoinAck", hx(w.MsgJoinAck))
	kv("MsgSpawn", hx(w.MsgSpawn))
	kv("MsgDeath", hx(w.MsgDeath))
	kv("MsgHit", hx(w.MsgHit))
	kv("MsgMatchState", hx(w.MsgMatchState))
	kv("MsgPickupState", hx(w.MsgPickupState))
	kv("MsgKillstreak", hx(w.MsgKillstreak))
	kv("MsgWeaponState", hx(w.MsgWeaponState))
	kv("MsgFlagState", hx(w.MsgFlagState))
	kv("MsgCapture", hx(w.MsgCapture))
	kv("MatchActive", dec(s.MatchActive))
	kv("MatchIntermission", dec(s.MatchIntermission))
	kv("PickupMedkit", dec(s.PickupMedkit))
	kv("PickupRapid", dec(s.PickupRapid))
	kv("PickupSpread", dec(s.PickupSpread))
	kv("WeaponPistol", dec(s.WeaponPistol))
	kv("WeaponShotgun", dec(s.WeaponShotgun))
	kv("WeaponSniper", dec(s.WeaponSniper))
	kv("WeaponRocket", dec(s.WeaponRocket))
	kv("BtnUp", hx(w.BtnUp))
	kv("BtnLeft", hx(w.BtnLeft))
	kv("BtnDown", hx(w.BtnDown))
	kv("BtnRight", hx(w.BtnRight))
	kv("BtnFire", hx(w.BtnFire))
	kv("WeaponSelectShift", dec(w.WeaponSelectShift))
	kv("WeaponSelectMask", hx(w.WeaponSelectMask))
	kv("ActDash", hx(w.ActDash))
	kv("FieldKind", hx(w.FieldKind))
	kv("FieldX", hx(w.FieldX))
	kv("FieldY", hx(w.FieldY))
	kv("FieldVX", hx(w.FieldVX))
	kv("FieldVY", hx(w.FieldVY))
	kv("FieldHP", hx(w.FieldHP))
	kv("FieldAll", hx(w.FieldAll))
	b.WriteString("};\n\n")

	fmt.Fprintf(&b, "const MATCH_TEAM_MODE = %s;\n", hx(w.MatchFlagTeamMode))
	fmt.Fprintf(&b, "const MATCH_HILL_MODE = %s;\n", hx(w.MatchFlagHillMode))
	fmt.Fprintf(&b, "const MATCH_DOM_MODE = %s;\n", hx(w.MatchFlagDomMode))
	fmt.Fprintf(&b, "const MATCH_CTF_MODE = %s;\n\n", hx(w.MatchFlagCtfMode))

	fmt.Fprintf(&b, "const COORD_SCALE = %d;\n", w.CoordScale)
	fmt.Fprintf(&b, "const INPUT_RATE = %d;\n", s.InputRate)
	fmt.Fprintf(&b, "const INV_SQRT2 = %s;\n\n", f32(s.InvSqrt2))

	b.WriteString("const SIM = {\n")
	kv("MapSize", f32(s.MapSize))
	kv("PlayerRadius", f32(s.PlayerRadius))
	kv("PlayerSpeed", f32(s.PlayerSpeed))
	kv("ProjectileRadius", f32(s.ProjectileRadius))
	kv("DashSpeedMult", f32(s.DashSpeedMult))
	kv("DashDuration", f32(s.DashDuration))
	kv("DashCooldown", f32(s.DashCooldown))
	kv("HillX", f32(s.HillX))
	kv("HillY", f32(s.HillY))
	kv("HillRadius", f32(s.HillRadius))
	kv("DomRadius", f32(s.DomRadius))
	b.WriteString("  DomPoints: [\n")
	for _, p := range s.DomPoints {
		fmt.Fprintf(&b, "    { x: %s, y: %s },\n", f32(p.X), f32(p.Y))
	}
	b.WriteString("  ],\n")
	kv("FlagBaseRadius", f32(s.FlagBaseRadius))
	b.WriteString("  FlagBases: [\n")
	for _, p := range s.FlagBases {
		fmt.Fprintf(&b, "    { x: %s, y: %s },\n", f32(p.X), f32(p.Y))
	}
	b.WriteString("  ],\n")
	b.WriteString("};\n\n")

	b.WriteString("const WALLS = [\n")
	for _, wl := range s.Walls {
		fmt.Fprintf(&b, "  { minX: %s, minY: %s, maxX: %s, maxY: %s },\n",
			f32(wl.MinX), f32(wl.MinY), f32(wl.MaxX), f32(wl.MaxY))
	}
	b.WriteString("];\n\n")

	b.WriteString("const PICKUP_SPOTS = [\n")
	for _, p := range s.PickupSpots {
		fmt.Fprintf(&b, "  { x: %s, y: %s },\n", f32(p.X), f32(p.Y))
	}
	b.WriteString("];\n")

	return b.String()
}

// hx форматирует байтовую константу (код/бит/маску) как 0x-литерал.
func hx(v uint8) string { return fmt.Sprintf("0x%02x", v) }

// dec форматирует небольшой индекс/сдвиг как десятичный литерал.
func dec(v uint8) string { return strconv.Itoa(int(v)) }

// f32 форматирует float32 кратчайшим round-trip-литералом (как хранит Go).
func f32(v float32) string { return strconv.FormatFloat(float64(v), 'g', -1, 32) }
