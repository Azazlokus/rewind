// Команда replay проигрывает записанный лог сессии headless и печатает Checksum
// итогового мира; с -verify сверяет его с эталоном и завершается ненулевым кодом
// при расхождении — так пойманный desync становится регрессионной проверкой.
// Итерация 7.
//
// Запуск: go run ./cmd/replay [-verify <hex>] <файл.replay>
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"arena/internal/game"
)

func main() {
	verify := flag.String("verify", "", "ожидаемый Checksum (hex); при расхождении exit 1")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay [-verify <hex>] <файл.replay>")
		os.Exit(2)
	}
	path := flag.Arg(0)

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: read %s: %v\n", path, err)
		os.Exit(1)
	}
	log, err := game.DecodeReplay(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: decode %s: %v\n", path, err)
		os.Exit(1)
	}
	sum, err := game.Replay(log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: seed=%d tickRate=%d ticks=%d events=%d checksum=%016x\n",
		path, log.Seed, log.TickRate, log.Ticks, log.Len(), sum)

	if *verify != "" {
		want, err := strconv.ParseUint(*verify, 16, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "replay: bad -verify %q: %v\n", *verify, err)
			os.Exit(2)
		}
		if sum != want {
			fmt.Fprintf(os.Stderr, "replay: DESYNC — checksum %016x != expected %016x\n", sum, want)
			os.Exit(1)
		}
		fmt.Println("replay: OK (checksum matches)")
	}
}
