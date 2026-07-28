//go:build integration

// Loopback-тест WebRTC-транспорта: сервер (AcceptWebRTC) и клиент (DialWebRTC),
// сигналинг по in-memory Pipe. Поднимает настоящий стек (ICE/DTLS/SCTP по
// host-кандидатам на localhost), поэтому идёт под тегом integration, а не в
// обязательном `make check`.
package transport

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v5"
)

func TestWebRTCLoopbackRoundTrip(t *testing.T) {
	serverSig, clientSig := Pipe(8)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type accepted struct {
		conn Conn
		err  error
	}
	accept := make(chan accepted, 1)
	go func() {
		c, err := AcceptWebRTC(ctx, serverSig, WebRTCConfig{})
		accept <- accepted{c, err}
	}()

	client, err := DialWebRTC(ctx, clientSig, WebRTCConfig{})
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer func() { _ = client.Close("done") }()

	res := <-accept
	if res.err != nil {
		t.Fatalf("server accept: %v", res.err)
	}
	server := res.conn
	defer func() { _ = server.Close("done") }()

	// Клиент → сервер.
	if err := client.Write(ctx, []byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if got, err := server.Read(ctx); err != nil {
		t.Fatalf("server read: %v", err)
	} else if string(got) != "hello" {
		t.Fatalf("server read %q, want hello", got)
	}

	// Сервер → клиент.
	if err := server.Write(ctx, []byte("world")); err != nil {
		t.Fatalf("server write: %v", err)
	}
	if got, err := client.Read(ctx); err != nil {
		t.Fatalf("client read: %v", err)
	} else if string(got) != "world" {
		t.Fatalf("client read %q, want world", got)
	}

	// Close будит висящий Read с ErrClosed.
	if err := server.Close("bye"); err != nil {
		t.Fatalf("server close: %v", err)
	}
	if _, err := server.Read(ctx); err == nil {
		t.Fatal("read after close returned nil error, want ErrClosed")
	}
}

// TestWebRTCUnreliableDelivers проверяет второй канал (итерация 12): сервер шлёт
// снапшот через UnreliableWriter (unordered+unreliable "state"), клиент читает его
// тем же Read. На loopback без потерь best-effort доставляет, поэтому round-trip
// детерминирован — доказывает, что unreliable-канал поднят и подключён к recv.
func TestWebRTCUnreliableDelivers(t *testing.T) {
	serverSig, clientSig := Pipe(8)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type accepted struct {
		conn Conn
		err  error
	}
	accept := make(chan accepted, 1)
	go func() {
		c, err := AcceptWebRTC(ctx, serverSig, WebRTCConfig{})
		accept <- accepted{c, err}
	}()

	client, err := DialWebRTC(ctx, clientSig, WebRTCConfig{})
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer func() { _ = client.Close("done") }()

	res := <-accept
	if res.err != nil {
		t.Fatalf("server accept: %v", res.err)
	}
	server := res.conn
	defer func() { _ = server.Close("done") }()

	u, ok := server.(UnreliableWriter)
	if !ok {
		t.Fatal("webrtc conn must implement UnreliableWriter")
	}
	if err := u.WriteUnreliable(ctx, []byte("snap")); err != nil {
		t.Fatalf("write unreliable: %v", err)
	}
	if got, err := client.Read(ctx); err != nil {
		t.Fatalf("client read: %v", err)
	} else if string(got) != "snap" {
		t.Fatalf("client read %q, want snap", got)
	}
}

// TestWebRTCTURNRelay поднимает in-process TURN-сервер (статические креды) и гонит
// DataChannel в режиме relay-only: host/srflx-кандидаты отброшены (ForceRelay),
// поэтому единственный способ соединиться — через TURN. Успешный round-trip
// доказывает, что TURN-путь (креды + relay-политика) работает от конфига до
// открытого канала. ForceRelay/креды заданы только серверу — клиент получает их
// через config-сигналинг (пустой WebRTCConfig).
func TestWebRTCTURNRelay(t *testing.T) {
	const user, pass, realm = "arena", "s3cr3t", "arena"

	relayPC, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen turn: %v", err)
	}
	port := relayPC.LocalAddr().(*net.UDPAddr).Port

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if ra.Username != user {
				return "", nil, false
			}
			return ra.Username, turn.GenerateAuthKey(user, ra.Realm, pass), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: relayPC,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("127.0.0.1"),
				Address:      "127.0.0.1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("turn server: %v", err)
	}
	defer func() { _ = srv.Close() }()

	turnURL := fmt.Sprintf("turn:127.0.0.1:%d?transport=udp", port)
	serverCfg := WebRTCConfig{
		ICEServers: []ICEServer{{URLs: []string{turnURL}, Username: user, Credential: pass}},
		ForceRelay: true,
	}

	serverSig, clientSig := Pipe(8)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type accepted struct {
		conn Conn
		err  error
	}
	accept := make(chan accepted, 1)
	go func() {
		c, err := AcceptWebRTC(ctx, serverSig, serverCfg)
		accept <- accepted{c, err}
	}()

	// Клиент — пустой конфиг: TURN-серверы и relay-политику берёт из config-сигналинга.
	client, err := DialWebRTC(ctx, clientSig, WebRTCConfig{})
	if err != nil {
		t.Fatalf("client dial (relay): %v", err)
	}
	defer func() { _ = client.Close("done") }()

	res := <-accept
	if res.err != nil {
		t.Fatalf("server accept (relay): %v", res.err)
	}
	server := res.conn
	defer func() { _ = server.Close("done") }()

	// Канал открылся только через relay — гоняем данные в обе стороны.
	if err := server.Write(ctx, []byte("relayed")); err != nil {
		t.Fatalf("server write: %v", err)
	}
	if got, err := client.Read(ctx); err != nil {
		t.Fatalf("client read: %v", err)
	} else if string(got) != "relayed" {
		t.Fatalf("client read %q, want relayed", got)
	}
}
