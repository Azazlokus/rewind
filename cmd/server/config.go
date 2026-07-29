package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"arena/internal/transport"
)

// serverConfig — конфигурация процесса, читаемая из окружения, чтобы бинарь
// запускался без флагов.
type serverConfig struct {
	Addr           string                // адрес прослушивания HTTP
	PprofAddr      string                // адрес pprof, пусто — отключить
	WebDir         string                // каталог, отдаваемый на /
	TickRate       int                   // Гц симуляции
	SnapshotRate   int                   // Гц снапшотов
	MaxPlayers     int                   // игроков на комнату
	MaxRooms       int                   // комнат на hub
	BotFill        int                   // держать столько игроков (люди+боты) в занятой комнате (0 — выкл)
	AOIRadius      float32               // радиус interest management, юниты (0 — выключено)
	Seed           int64                 // seed мира
	JoinTimeout    time.Duration         // сколько у клиента есть на отправку Join
	ShutdownGrace  time.Duration         // дедлайн чистого HTTP-shutdown
	AllowAllOrigin bool                  // dev: пропускать проверки origin WebSocket
	ICEServers     []transport.ICEServer // STUN/TURN для WebRTC (пусто — только host, localhost/LAN)
	ForceRelay     bool                  // WebRTC только через TURN-relay (жёсткие сети/приватность)
	DBDriver       string                // бэкенд хранилища: "sqlite" (dev/CI) или "postgres" (prod)
	DBDSN          string                // строка подключения: путь к файлу SQLite или DSN Postgres
	AuthSecret     []byte                // ключ подписи токен-сессий (пусто — эфемерный на запуск)
	TokenTTL       time.Duration         // время жизни токена
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
		AOIRadius:      640, // ±640 покрывает экран 800×600 с запасом; 0 — выключить
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
	if c.BotFill, err = getenvInt("ARENA_BOT_FILL", c.BotFill); err != nil {
		return c, err
	}
	if c.AOIRadius, err = getenvFloat("ARENA_AOI_RADIUS", c.AOIRadius); err != nil {
		return c, err
	}
	seed, err := getenvInt("ARENA_SEED", int(c.Seed))
	if err != nil {
		return c, err
	}
	c.Seed = int64(seed)

	c.ICEServers = iceServersFromEnv()
	c.ForceRelay = getenvBool("ARENA_FORCE_RELAY", false)

	c.DBDriver = getenv("ARENA_DB_DRIVER", "sqlite")
	c.DBDSN = getenv("ARENA_DB_DSN", "arena.db")
	if s := getenv("ARENA_AUTH_SECRET", ""); s != "" {
		c.AuthSecret = []byte(s)
	}
	if c.TokenTTL, err = getenvDuration("ARENA_TOKEN_TTL", 24*time.Hour); err != nil {
		return c, err
	}

	c.LogLevel = parseLevel(getenv("ARENA_LOG_LEVEL", "info"))
	return c, nil
}

// getenvDuration читает time.Duration (например, "24h", "30m"); пусто — def.
func getenvDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("config: %s=%q: %w", key, v, err)
	}
	return d, nil
}

// iceServersFromEnv собирает ICE-серверы для WebRTC из окружения:
//   - ARENA_STUN — STUN URL через запятую (без кредов).
//   - ARENA_TURN — TURN URL через запятую; ARENA_TURN_USER/ARENA_TURN_PASS — их
//     статические креды (нужны для обхода NAT, где host/STUN не проходят).
//
// Пусто — nil: только host-кандидаты (localhost/LAN, наружу никто не ходит).
func iceServersFromEnv() []transport.ICEServer {
	var servers []transport.ICEServer
	if stun := splitList(getenv("ARENA_STUN", "")); len(stun) > 0 {
		servers = append(servers, transport.ICEServer{URLs: stun})
	}
	if turn := splitList(getenv("ARENA_TURN", "")); len(turn) > 0 {
		servers = append(servers, transport.ICEServer{
			URLs:       turn,
			Username:   getenv("ARENA_TURN_USER", ""),
			Credential: getenv("ARENA_TURN_PASS", ""),
		})
	}
	return servers
}

// splitList разбивает список через запятую, отбрасывая пустые элементы и пробелы.
// Пустая строка даёт nil — «без ICE-серверов».
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
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

func getenvFloat(key string, def float32) (float32, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 32)
	if err != nil {
		return def, fmt.Errorf("config: %s=%q: %w", key, v, err)
	}
	return float32(f), nil
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
