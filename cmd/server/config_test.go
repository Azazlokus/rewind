package main

import (
	"reflect"
	"testing"

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
