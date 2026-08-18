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

// TestAuthTTLConfig проверяет чтение ARENA_ACCESS_TTL / ARENA_REFRESH_TTL (итерация 36):
// короткий access по умолчанию, длинный refresh, оба переопределяются из env.
func TestAuthTTLConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("ARENA_ACCESS_TTL", "")
		t.Setenv("ARENA_REFRESH_TTL", "")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.AccessTTL != 15*time.Minute || cfg.RefreshTTL != 30*24*time.Hour {
			t.Fatalf("default TTLs = %v / %v, want 15m / 720h", cfg.AccessTTL, cfg.RefreshTTL)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("ARENA_ACCESS_TTL", "5m")
		t.Setenv("ARENA_REFRESH_TTL", "168h")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.AccessTTL != 5*time.Minute || cfg.RefreshTTL != 168*time.Hour {
			t.Fatalf("TTLs = %v / %v, want 5m / 168h", cfg.AccessTTL, cfg.RefreshTTL)
		}
	})
}

// TestJoinRateConfig проверяет чтение ARENA_JOIN_* (итерация 33): по умолчанию
// включён (кап 16, burst 30, окно 60с, без заголовка), значения из env прокидываются,
// нули выключают.
func TestJoinRateConfig(t *testing.T) {
	joinEnv := []string{"ARENA_JOIN_MAX_PER_IP", "ARENA_JOIN_RATE_BURST", "ARENA_JOIN_RATE_WINDOW", "ARENA_JOIN_RATE_IP_HEADER"}
	t.Run("defaults enabled", func(t *testing.T) {
		for _, k := range joinEnv {
			t.Setenv(k, "")
		}
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.JoinMaxPerIP != 16 || cfg.JoinRateBurst != 30 || cfg.JoinRateWindow != time.Minute || cfg.JoinRateHeader != "" {
			t.Fatalf("defaults = {max %d, burst %d, window %v, header %q}, want {16, 30, 1m, \"\"}",
				cfg.JoinMaxPerIP, cfg.JoinRateBurst, cfg.JoinRateWindow, cfg.JoinRateHeader)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("ARENA_JOIN_MAX_PER_IP", "4")
		t.Setenv("ARENA_JOIN_RATE_BURST", "5")
		t.Setenv("ARENA_JOIN_RATE_WINDOW", "10s")
		t.Setenv("ARENA_JOIN_RATE_IP_HEADER", "X-Forwarded-For")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.JoinMaxPerIP != 4 || cfg.JoinRateBurst != 5 || cfg.JoinRateWindow != 10*time.Second || cfg.JoinRateHeader != "X-Forwarded-For" {
			t.Fatalf("from env = {max %d, burst %d, window %v, header %q}, want {4, 5, 10s, X-Forwarded-For}",
				cfg.JoinMaxPerIP, cfg.JoinRateBurst, cfg.JoinRateWindow, cfg.JoinRateHeader)
		}
	})
	t.Run("zeros disable", func(t *testing.T) {
		t.Setenv("ARENA_JOIN_MAX_PER_IP", "0")
		t.Setenv("ARENA_JOIN_RATE_BURST", "0")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.JoinMaxPerIP != 0 || cfg.JoinRateBurst != 0 {
			t.Fatalf("zeros = {max %d, burst %d}, want {0, 0} (disabled)", cfg.JoinMaxPerIP, cfg.JoinRateBurst)
		}
	})
}

// TestTracingConfig проверяет чтение ARENA_OTEL_* (итерация 34): по умолчанию выключено,
// ratio 1.0, insecure, имя сервиса arena-server; значения из env прокидываются.
func TestTracingConfig(t *testing.T) {
	otelEnv := []string{"ARENA_OTEL_ENABLED", "ARENA_OTEL_ENDPOINT", "ARENA_OTEL_INSECURE", "ARENA_OTEL_STDOUT", "ARENA_OTEL_SAMPLE_RATIO", "ARENA_OTEL_SERVICE_NAME"}
	t.Run("defaults disabled", func(t *testing.T) {
		for _, k := range otelEnv {
			t.Setenv(k, "")
		}
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.TracingEnabled || cfg.TracingStdout || cfg.TracingEndpoint != "" ||
			!cfg.TracingInsecure || cfg.TracingSampleRatio != 1.0 || cfg.TracingService != "arena-server" {
			t.Fatalf("defaults = {enabled %v, stdout %v, endpoint %q, insecure %v, ratio %v, service %q}, want {false, false, \"\", true, 1.0, arena-server}",
				cfg.TracingEnabled, cfg.TracingStdout, cfg.TracingEndpoint, cfg.TracingInsecure, cfg.TracingSampleRatio, cfg.TracingService)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("ARENA_OTEL_ENABLED", "true")
		t.Setenv("ARENA_OTEL_ENDPOINT", "jaeger:4318")
		t.Setenv("ARENA_OTEL_INSECURE", "true")
		t.Setenv("ARENA_OTEL_SAMPLE_RATIO", "0.25")
		t.Setenv("ARENA_OTEL_SERVICE_NAME", "arena-test")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if !cfg.TracingEnabled || cfg.TracingEndpoint != "jaeger:4318" || cfg.TracingSampleRatio != 0.25 || cfg.TracingService != "arena-test" {
			t.Fatalf("from env = {enabled %v, endpoint %q, ratio %v, service %q}, want {true, jaeger:4318, 0.25, arena-test}",
				cfg.TracingEnabled, cfg.TracingEndpoint, cfg.TracingSampleRatio, cfg.TracingService)
		}
	})
	t.Run("bad ratio errors", func(t *testing.T) {
		t.Setenv("ARENA_OTEL_SAMPLE_RATIO", "not-a-number")
		if _, err := loadConfig(); err == nil {
			t.Fatal("invalid ARENA_OTEL_SAMPLE_RATIO should error")
		}
	})
}
