//go:build integration

// Loopback-тест WebRTC-транспорта: сервер (AcceptWebRTC) и клиент (DialWebRTC),
// сигналинг по in-memory Pipe. Поднимает настоящий стек (ICE/DTLS/SCTP по
// host-кандидатам на localhost), поэтому идёт под тегом integration, а не в
// обязательном `make check`.
package transport

import (
	"context"
	"testing"
	"time"
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
