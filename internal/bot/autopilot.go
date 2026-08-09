package bot

import (
	"context"
	rand "math/rand/v2"
	"time"

	"arena/internal/protocol"
)

// moveDirs — восемь направлений движения (WASD и диагонали).
var moveDirs = []uint8{
	protocol.BtnUp, protocol.BtnDown, protocol.BtnLeft, protocol.BtnRight,
	protocol.BtnUp | protocol.BtnLeft, protocol.BtnUp | protocol.BtnRight,
	protocol.BtnDown | protocol.BtnLeft, protocol.BtnDown | protocol.BtnRight,
}

// Drain вычерпывает снапшоты, пока живо соединение: реконструирует дельты,
// подтверждает тики (чтобы сервер слал дельты) и не даёт комнате счесть клиента
// отставшим. Возвращается, когда соединение закрыто или ctx отменён — то есть сам
// по себе не завершается, владелец гасит его отменой контекста или закрытием conn.
func Drain(ctx context.Context, c *Client) {
	for {
		if _, err := c.ReadSnapshot(ctx); err != nil {
			return // соединение закрыто (shutdown/снятие) — выходим
		}
	}
}

// Autopilot гоняет простое поведение бота на 60 Гц: раз в ~0.5 с меняет
// направление, прицел плавно вращается, ~15% вводов — огонь (кулдаун сервера
// отсечёт лишнее). RNG-поток детерминирован переданным rng; тайминг вводов идёт по
// реальным часам (ticker), поэтому прогон побайтно не воспроизводим — это нагрузка
// и наполнение комнат, а не harness детерминизма. Возвращается по отмене ctx или
// закрытию соединения; владелец горутины — вызывающий.
func Autopilot(ctx context.Context, c *Client, rng *rand.Rand) {
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
