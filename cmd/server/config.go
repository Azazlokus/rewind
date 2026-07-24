package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// serverConfig is the process configuration, read from the environment so the
// binary needs no flags to run.
type serverConfig struct {
	Addr           string        // HTTP listen address
	PprofAddr      string        // pprof listen address, empty disables it
	WebDir         string        // directory served at /
	TickRate       int           // simulation Hz
	SnapshotRate   int           // snapshot Hz
	MaxPlayers     int           // players per room
	MaxRooms       int           // rooms per hub
	Seed           int64         // world seed
	JoinTimeout    time.Duration // how long a client has to send its Join
	ShutdownGrace  time.Duration // deadline for a clean HTTP shutdown
	AllowAllOrigin bool          // dev: skip WebSocket origin checks
	LogLevel       slog.Level
}

// loadConfig reads the configuration from the environment, applying defaults.
func loadConfig() (serverConfig, error) {
	c := serverConfig{
		Addr:           getenv("ARENA_ADDR", ":8080"),
		PprofAddr:      getenv("ARENA_PPROF_ADDR", "127.0.0.1:6060"),
		WebDir:         getenv("ARENA_WEB_DIR", "web"),
		TickRate:       30,
		SnapshotRate:   30, // iteration 1 sends every tick; iteration 2 drops to 20
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
