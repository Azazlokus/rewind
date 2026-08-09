package main

import (
	"reflect"
	"testing"
	"time"

	"arena/internal/transport"
)

// TestICEServersFromEnv проверяет сборку STUN/TURN-серверов из окружения (итерация
// 12): STUN — без кредов, TURN — с Username/Credential, пусто — nil.
func TestICEServersFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []transport.ICEServer
	}{
		{
			name: "empty",
			env:  nil,
			want: nil,
		},
		{
			name: "stun only",
			env:  map[string]string{"ARENA_STUN": "stun:a:1, stun:b:2"},
			want: []transport.ICEServer{{URLs: []string{"stun:a:1", "stun:b:2"}}},
		},
		{
			name: "turn with creds",
			env: map[string]string{
				"ARENA_TURN":      "turn:t:3",
				"ARENA_TURN_USER": "u",
				"ARENA_TURN_PASS": "p",
			},
			want: []transport.ICEServer{{URLs: []string{"turn:t:3"}, Username: "u", Credential: "p"}},
		},
		{
			name: "stun and turn",
			env: map[string]string{
				"ARENA_STUN":      "stun:a:1",
				"ARENA_TURN":      "turn:t:3",
				"ARENA_TURN_USER": "u",
				"ARENA_TURN_PASS": "p",
			},
			want: []transport.ICEServer{
				{URLs: []string{"stun:a:1"}},
				{URLs: []string{"turn:t:3"}, Username: "u", Credential: "p"},
			},
		},
	}

	// Ключи, которые тесты выставляют; чистим перед каждым кейсом через t.Setenv.
	keys := []string{"ARENA_STUN", "ARENA_TURN", "ARENA_TURN_USER", "ARENA_TURN_PASS"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range keys {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := iceServersFromEnv()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("iceServersFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestBotFillConfig проверяет чтение ARENA_BOT_FILL (итерация 17): по умолчанию 0
// (наполнитель выключен), заданное значение прокидывается.
func TestBotFillConfig(t *testing.T) {
	t.Run("default disabled", func(t *testing.T) {
		t.Setenv("ARENA_BOT_FILL", "")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.BotFill != 0 {
			t.Fatalf("default BotFill = %d, want 0", cfg.BotFill)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("ARENA_BOT_FILL", "6")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.BotFill != 6 {
			t.Fatalf("BotFill = %d, want 6", cfg.BotFill)
		}
	})
}

// TestAuthRateConfig проверяет чтение ARENA_AUTH_RATE_* (итерация 21): по умолчанию
// включён (burst 10, window 60с), значения из env прокидываются, burst=0 выключает.
func TestAuthRateConfig(t *testing.T) {
	t.Run("defaults enabled", func(t *testing.T) {
		for _, k := range []string{"ARENA_AUTH_RATE_BURST", "ARENA_AUTH_RATE_WINDOW", "ARENA_AUTH_RATE_IP_HEADER"} {
			t.Setenv(k, "")
		}
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.AuthRate.Burst != 10 || cfg.AuthRate.Window != time.Minute || cfg.AuthRate.ClientIPHeader != "" {
			t.Fatalf("default AuthRate = %+v, want {10, 1m, \"\"}", cfg.AuthRate)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("ARENA_AUTH_RATE_BURST", "3")
		t.Setenv("ARENA_AUTH_RATE_WINDOW", "30s")
		t.Setenv("ARENA_AUTH_RATE_IP_HEADER", "X-Forwarded-For")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.AuthRate.Burst != 3 || cfg.AuthRate.Window != 30*time.Second || cfg.AuthRate.ClientIPHeader != "X-Forwarded-For" {
			t.Fatalf("AuthRate = %+v, want {3, 30s, X-Forwarded-For}", cfg.AuthRate)
		}
	})
	t.Run("burst zero disables", func(t *testing.T) {
		t.Setenv("ARENA_AUTH_RATE_BURST", "0")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.AuthRate.Burst != 0 {
			t.Fatalf("AuthRate.Burst = %d, want 0 (disabled)", cfg.AuthRate.Burst)
		}
	})
}
