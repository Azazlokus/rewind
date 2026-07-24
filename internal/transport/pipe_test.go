package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPipeRoundTrip(t *testing.T) {
	server, client := Pipe(4)
	ctx := context.Background()

	if err := client.Write(ctx, []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := server.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want hello", got)
	}
}

// TestPipeCopiesBuffer проверяет, что читатель не ссылается на буфер писателя —
// значит, вызывающий может переиспользовать срез, переданный в Write.
func TestPipeCopiesBuffer(t *testing.T) {
	server, client := Pipe(4)
	ctx := context.Background()

	buf := []byte("abc")
	if err := client.Write(ctx, buf); err != nil {
		t.Fatal(err)
	}
	buf[0] = 'X' // изменяем после возврата Write
	got, err := server.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" {
		t.Fatalf("write did not copy: got %q", got)
	}
}

// TestPipeCloseUnblocksRead проверяет, что заблокированный читатель освобождается
// при закрытии пира, с ErrClosed.
func TestPipeCloseUnblocksRead(t *testing.T) {
	server, client := Pipe(0)

	done := make(chan error, 1)
	go func() {
		_, err := server.Read(context.Background())
		done <- err
	}()

	// Даём читателю мгновение заблокироваться, затем закрываем дальний конец.
	_ = client.Close("done")

	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("got %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not unblock on peer close")
	}
}

// TestPipeDrainsAfterClose проверяет, что уже летящие сообщения доставляются
// после закрытия пира — тест никогда не теряет последний снапшот.
func TestPipeDrainsAfterClose(t *testing.T) {
	server, client := Pipe(4)
	ctx := context.Background()

	if err := client.Write(ctx, []byte("last")); err != nil {
		t.Fatal(err)
	}
	_ = client.Close("done")

	got, err := server.Read(ctx)
	if err != nil {
		t.Fatalf("expected buffered message, got error %v", err)
	}
	if string(got) != "last" {
		t.Fatalf("got %q, want last", got)
	}
}

func TestPipeContextCancel(t *testing.T) {
	server, _ := Pipe(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
