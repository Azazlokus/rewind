// Команда loadtest прогоняет N ботов в одной комнате in-process и измеряет, как
// сервер держит нагрузку: длительность тика (p50/p99/max), число сущностей в
// снапшоте (эффект interest management) и исходящий трафик на клиента (эффект
// дельт). Итерация 6C.
//
// Боты подключаются через transport.Pipe, а не по сети: тик p99 — это работа
// горутины комнаты (drain + Step + broadcast + encode), сокет в неё не входит,
// поэтому Pipe даёт ту же тик-стоимость без сетевого джиттера и портов, in-process
// и воспроизводимо. Комната крутится на реальных часах (30 Гц), боты шлют ввод на
// 60 Гц — настоящий real-time прогон.
//
// Запуск: make loadtest  (go run ./cmd/loadtest -bots=200 -duration=60s)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	rand "math/rand/v2"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"arena/internal/bot"
	"arena/internal/game"
	"arena/internal/protocol"
	"arena/internal/transport"
)

func main() {
	bots := flag.Int("bots", 200, "число ботов в комнате")
	duration := flag.Duration("duration", 60*time.Second, "длительность прогона")
	tickRate := flag.Int("tick", 30, "частота симуляции, Гц")
	snapRate := flag.Int("snapshot", 20, "частота снапшотов, Гц")
	aoi := flag.Float64("aoi", 640, "радиус interest management в юнитах (0 — выкл)")
	seed := flag.Int64("seed", 1, "seed мира и ботов (воспроизводимость)")
	replayPath := flag.String("replay", "", "записать лог реплея в файл (пусто — не писать)")
	flag.Parse()

	st := newStats()
	ctx, cancel := context.WithCancel(context.Background())

	room := game.NewRoom("loadtest", game.Config{
		TickRate:     *tickRate,
		SnapshotRate: *snapRate,
		MaxPlayers:   *bots,
		AOIRadius:    float32(*aoi),
		Seed:         *seed,
		Metrics:      st,
		RecordReplay: *replayPath != "",
	})
	go room.Run(ctx)
	<-room.Ready()

	fmt.Printf("loadtest: %d ботов, %s, tick=%dГц snapshot=%dГц aoi=%.0f seed=%d\n",
		*bots, *duration, *tickRate, *snapRate, *aoi, *seed)

	var wg sync.WaitGroup
	for i := 0; i < *bots; i++ {
		c, err := connectBot(ctx, room, fmt.Sprintf("bot%d", i), &wg)
		if err != nil {
			log.Fatalf("loadtest: connect bot %d: %v", i, err)
		}
		rng := rand.New(rand.NewPCG(uint64(*seed), uint64(i)+1))
		wg.Add(2)
		go func() { defer wg.Done(); runReader(ctx, c) }()
		go func() { defer wg.Done(); runDriver(ctx, c, rng) }()
	}
	fmt.Printf("loadtest: %d ботов подключены, прогон %s…\n", *bots, *duration)

	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	time.Sleep(*duration)
	cancel()      // останавливает комнату; она закрывает сессии, что роняет ботов
	<-room.Done() // ждём остановки комнаты (валиден финальный замер метрик)
	wg.Wait()
	runtime.ReadMemStats(&m1)

	st.report(*bots, *duration, *tickRate)
	// Аллокации за прогон. RNG-поток ботов задан seed'ом, но прогон побайтно НЕ
	// воспроизводим (тайминг вводов — по реальным часам), поэтому числа близки, а не
	// идентичны; разницу между прогонами с пулом и без доминирует экономия пула, не шум.
	fmt.Printf("аллокаций за прогон:   %d (GC: %d)\n", m1.Mallocs-m0.Mallocs, m1.NumGC-m0.NumGC)
	fmt.Printf("dropped inputs:        %d\n", room.DroppedInputs())

	// Лог реплея (если запись включена) — безопасно после <-room.Done().
	if *replayPath != "" {
		log := room.ReplayLog()
		if err := os.WriteFile(*replayPath, log.Encode(), 0o644); err != nil {
			fmt.Printf("не удалось записать реплей: %v\n", err)
		} else {
			fmt.Printf("реплей записан:        %s (%d событий; проверка: go run ./cmd/replay %s)\n",
				*replayPath, log.Len(), *replayPath)
		}
	}
}

// connectBot поднимает бота на in-process канале: серверный конец идёт в room.Join
// (тот присоединяет игрока и ставит в очередь JoinAck), клиентский — в bot.Attach.
// Горутина сессии учитывается в wg — владелец (main) дожидается её завершения.
func connectBot(ctx context.Context, room *game.Room, name string, wg *sync.WaitGroup) (*bot.Client, error) {
	server, client := transport.Pipe(64)
	sess, err := room.Join(ctx, server, name)
	if err != nil {
		_ = server.Close("join failed")
		_ = client.Close("join failed")
		return nil, err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Ошибку сессии, кроме обычного закрытия соединения и отмены контекста,
		// показываем: молча глотать сбой нельзя (иначе kick-as-laggard/сбой кодека в
		// прогоне станет невидимым).
		if err := sess.Run(ctx); err != nil && !errors.Is(err, transport.ErrClosed) && ctx.Err() == nil {
			log.Printf("loadtest: session %s: %v", name, err)
		}
	}()
	return bot.Attach(ctx, client)
}

// runReader вычерпывает снапшоты: реконструирует дельты, подтверждает тики (чтобы
// сервер слал дельты) и не даёт комнате счесть бота отставшим.
func runReader(ctx context.Context, c *bot.Client) {
	for {
		if _, err := c.ReadSnapshot(ctx); err != nil {
			return // соединение закрыто (shutdown) — выходим
		}
	}
}

// moveDirs — восемь направлений движения (WASD и диагонали).
var moveDirs = []uint8{
	protocol.BtnUp, protocol.BtnDown, protocol.BtnLeft, protocol.BtnRight,
	protocol.BtnUp | protocol.BtnLeft, protocol.BtnUp | protocol.BtnRight,
	protocol.BtnDown | protocol.BtnLeft, protocol.BtnDown | protocol.BtnRight,
}

// runDriver — автопилот бота на 60 Гц: раз в ~0.5 с меняет направление, прицел
// плавно вращается, ~15% вводов — огонь (кулдаун сервера отсечёт лишнее). RNG-поток
// детерминирован per-bot seed'ом; тайминг вводов идёт по реальным часам (ticker),
// поэтому прогон побайтно не воспроизводим — это нагрузка, а не harness детерминизма.
func runDriver(ctx context.Context, c *bot.Client, rng *rand.Rand) {
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()

	dir := moveDirs[rng.IntN(len(moveDirs))]
	aimStep := uint16(400 + rng.IntN(400)) // своя скорость вращения прицела
	var aim uint16
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if n%30 == 0 {
				dir = moveDirs[rng.IntN(len(moveDirs))]
			}
			aim += aimStep
			buttons := dir
			if rng.IntN(100) < 15 {
				buttons |= protocol.BtnFire
			}
			if err := c.SendInput(ctx, buttons, aim); err != nil {
				return // соединение закрыто — выходим
			}
			n++
		}
	}
}

// stats собирает телеметрию комнаты. Все методы Recorder зовёт единственная
// горутина комнаты, поэтому синхронизация не нужна; report читается уже после
// остановки комнаты (<-room.Done() даёт happens-before).
type stats struct {
	ticks    []time.Duration // длительность каждого тика
	entSum   int64           // сумма сущностей по снапшотам
	entCount int64           // число снапшотов
	entMax   int64           // максимум сущностей в снапшоте
	bytesSum int64           // суммарные байты снапшотов
}

func newStats() *stats { return &stats{} }

func (s *stats) TickDuration(d time.Duration) { s.ticks = append(s.ticks, d) }
func (s *stats) SnapshotBytes(n int)          { s.bytesSum += int64(n) }
func (s *stats) EntitiesPerSnapshot(n int) {
	s.entSum += int64(n)
	s.entCount++
	if int64(n) > s.entMax {
		s.entMax = int64(n)
	}
}
func (s *stats) ConnectedPlayers(int) {}
func (s *stats) InboxDepth(int)       {}

func (s *stats) report(bots int, dur time.Duration, tickRate int) {
	if len(s.ticks) == 0 {
		fmt.Println("нет данных: комната не сделала ни одного тика")
		return
	}
	sort.Slice(s.ticks, func(i, j int) bool { return s.ticks[i] < s.ticks[j] })
	pct := func(p int) time.Duration { return s.ticks[(len(s.ticks)*p)/100] }
	p50, p99, mx := pct(50), pct(99), s.ticks[len(s.ticks)-1]

	avgEnt := 0.0
	if s.entCount > 0 {
		avgEnt = float64(s.entSum) / float64(s.entCount)
	}
	bytesPerClient := float64(s.bytesSum) / dur.Seconds() / float64(bots)
	budget := time.Second / time.Duration(tickRate)

	fmt.Println("--- результаты ---")
	fmt.Printf("тиков просчитано:      %d\n", len(s.ticks))
	fmt.Printf("длительность тика:     p50=%v  p99=%v  max=%v  (бюджет тика %v, цель p99<15ms)\n",
		p50.Round(time.Microsecond), p99.Round(time.Microsecond), mx.Round(time.Microsecond), budget)
	fmt.Printf("сущностей в снапшоте:  avg=%.1f  max=%d\n", avgEnt, s.entMax)
	fmt.Printf("трафик на клиента:     %.1f КБ/с (снапшоты)\n", bytesPerClient/1024)
}
