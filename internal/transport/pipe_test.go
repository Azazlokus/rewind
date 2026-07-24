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

// TestPipeCopiesBuffer checks the reader is not aliased to the writer's buffer,
// so a caller may reuse the slice it passed to Write.
func TestPipeCopiesBuffer(t *testing.T) {
	server, client := Pipe(4)
	ctx := context.Background()

	buf := []byte("abc")
	if err := client.Write(ctx, buf); err != nil {
		t.Fatal(err)
	}
	buf[0] = 'X' // mutate after Write returns
	got, err := server.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" {
		t.Fatalf("write did not copy: got %q", got)
	}
}

// TestPipeCloseUnblocksRead checks a blocked reader is released when the peer
// closes, with ErrClosed.
func TestPipeCloseUnblocksRead(t *testing.T) {
	server, client := Pipe(0)

	done := make(chan error, 1)
	go func() {
		_, err := server.Read(context.Background())
		done <- err
	}()

	// Give the reader a moment to block, then close the far end.
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

// TestPipeDrainsAfterClose checks messages already in flight are still delivered
// after the peer closes, so a test never loses the last snapshot.
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
