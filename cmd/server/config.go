package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// serverConfig — конфигурация процесса, читаемая из окружения, чтобы бинарь
// запускался без флагов.
type serverConfig struct {
	Addr           string        // адрес прослушивания HTTP
	PprofAddr      string        // адрес pprof, пусто — отключить
	WebDir         string        // каталог, отдаваемый на /
	TickRate       int           // Гц симуляции
	SnapshotRate   int           // Гц снапшотов
	MaxPlayers     int           // игроков на комнату
	MaxRooms       int           // комнат на hub
	Seed           int64         // seed мира
	JoinTimeout    time.Duration // сколько у клиента есть на отправку Join
	ShutdownGrace  time.Duration // дедлайн чистого HTTP-shutdown
	AllowAllOrigin bool          // dev: пропускать проверки origin WebSocket
	LogLevel       slog.Level
}

// loadConfig читает конфигурацию из окружения, применяя значения по умолчанию.
func loadConfig() (serverConfig, error) {
	c := serverConfig{
		Addr:           getenv("ARENA_ADDR", ":8080"),
		PprofAddr:      getenv("ARENA_PPROF_ADDR", "127.0.0.1:6060"),
		WebDir:         getenv("ARENA_WEB_DIR", "web"),
		TickRate:       30,
		SnapshotRate:   20, // итерация 2: снапшоты реже тикрейта, интерполяция это скрывает
		MaxPlayers:     64,
		MaxRooms:       16,
		Seed:           1,
		JoinTimeout:    5 * time.Second,
		ShutdownGrace:  5 * time.Second,
		AllowAllOrigin: getenvBool("ARENA_ALLOW_ALL_ORIGIN", true),
	}

	var err error
	if c.TickRate, err = getenvInt("ARENA_TICK_RATE", c.TickRate); err != nil {
		return c, err
	}
	if c.SnapshotRate, err = getenvInt("ARENA_SNAPSHOT_RATE", c.SnapshotRate); err != nil {
		return c, err
	}
	if c.MaxPlayers, err = getenvInt("ARENA_MAX_PLAYERS", c.MaxPlayers); err != nil {
		return c, err
	}
	if c.MaxRooms, err = getenvInt("ARENA_MAX_ROOMS", c.MaxRooms); err != nil {
		return c, err
	}
	seed, err := getenvInt("ARENA_SEED", int(c.Seed))
	if err != nil {
		return c, err
	}
	c.Seed = int64(seed)

	c.LogLevel = parseLevel(getenv("ARENA_LOG_LEVEL", "info"))
	return c, nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("config: %s=%q: %w", key, v, err)
	}
	return n, nil
}

func getenvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
