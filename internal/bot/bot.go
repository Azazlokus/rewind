// Package bot is a headless client. Iteration 1 provides just enough of one to
// drive the end-to-end integration test — dial, join, send input, read
// snapshots. Iteration 6 grows it into the load-test swarm (random movement and
// fire) without changing this surface.
package bot

import (
	"context"
	"fmt"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// Client is a single headless connection to a server.
type Client struct {
	conn transport.Conn
	id   uint16
	tick uint32
	seq  uint32
	// decode scratch, reused so ReadSnapshot stays allocation-light.
	msg protocol.ServerMessage
}

// Dial connects to url (e.g. ws://host/ws), performs the join handshake with the
// given name and returns once the server has acknowledged it.
func Dial(ctx context.Context, url, name string) (*Client, error) {
	conn, err := transport.Dial(ctx, url, transport.WSOptions{WriteKind: transport.KindText})
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn}

	join, err := protocol.AppendJoin(nil, protocol.Join{Name: name})
	if err != nil {
		_ = conn.Close("encode join")
		return nil, err
	}
	if err := conn.Write(ctx, join); err != nil {
		_ = conn.Close("write join")
		return nil, fmt.Errorf("bot: send join: %w", err)
	}

	// The first server message must be the JoinAck.
	if err := c.readInto(ctx); err != nil {
		_ = conn.Close("read ack")
		return nil, err
	}
	if c.msg.Type != protocol.MsgJoinAck {
		_ = conn.Close("bad handshake")
		return nil, fmt.Errorf("bot: expected JoinAck, got %v", c.msg.Type)
	}
	c.id = c.msg.JoinAck.YourID
	c.tick = c.msg.JoinAck.Tick
	return c, nil
}

// ID is the player id assigned by the server.
func (c *Client) ID() uint16 { return c.id }

// SendInput sends one command, assigning the next sequence number.
func (c *Client) SendInput(ctx context.Context, buttons uint8, aim uint16) error {
	c.seq++
	buf, err := protocol.AppendInput(nil, protocol.Input{Seq: c.seq, Buttons: buttons, Aim: aim})
	if err != nil {
		return err
	}
	if err := c.conn.Write(ctx, buf); err != nil {
		return fmt.Errorf("bot: send input: %w", err)
	}
	return nil
}

// Seq is the sequence number of the last input sent.
func (c *Client) Seq() uint32 { return c.seq }

// ReadSnapshot blocks until the next snapshot arrives, skipping reliable events.
// The returned Snapshot is valid until the next ReadSnapshot call.
func (c *Client) ReadSnapshot(ctx context.Context) (protocol.Snapshot, error) {
	for {
		if err := c.readInto(ctx); err != nil {
			return protocol.Snapshot{}, err
		}
		if c.msg.Type == protocol.MsgSnapshot {
			c.tick = c.msg.Snapshot.Tick
			return c.msg.Snapshot, nil
		}
	}
}

func (c *Client) readInto(ctx context.Context) error {
	data, err := c.conn.Read(ctx)
	if err != nil {
		return err
	}
	if err := protocol.DecodeServer(data, &c.msg); err != nil {
		return fmt.Errorf("bot: decode: %w", err)
	}
	return nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close("bot closed") }
