// Package discord sets Rich Presence through the Discord desktop client's
// local IPC socket ($TMPDIR/discord-ipc-N).
package discord

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2

	socketCount = 10
	ioTimeout   = 5 * time.Second
)

// ErrNotRunning is returned when no Discord IPC socket accepts the handshake.
var ErrNotRunning = errors.New("discord: no IPC socket available")

// ErrRejected is returned when Discord refuses an activity, e.g. because a
// field is invalid. Sending the same activity again fails the same way.
var ErrRejected = errors.New("discord: activity rejected")

// Client is a lazily connecting IPC client. It is safe for concurrent use.
type Client struct {
	ClientID  string
	SocketDir string // defaults to $TMPDIR

	mu        sync.Mutex
	conn      net.Conn
	responses chan response // from the connection's reader; closed when it stops
	lost      chan struct{}
}

// Lost receives when Discord closes the connection, e.g. because it quit.
// Discord then forgets the activity; the next SetActivity reconnects.
func (c *Client) Lost() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lostLocked()
}

func (c *Client) lostLocked() chan struct{} {
	if c.lost == nil {
		c.lost = make(chan struct{}, 1)
	}
	return c.lost
}

// SetActivity sets the activity, or clears it when a is nil.
// A failed call drops the connection; the next call reconnects.
func (c *Client) SetActivity(ctx context.Context, a *Activity) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		if err := c.connect(ctx); err != nil {
			return err
		}
	}
	n := nonce()
	payload := map[string]any{
		"cmd":   "SET_ACTIVITY",
		"args":  map[string]any{"pid": os.Getpid(), "activity": a},
		"nonce": n,
	}
	resp, err := c.command(ctx, payload, n)
	if err != nil {
		c.closeLocked()
		return fmt.Errorf("discord: set activity: %w", err)
	}
	if resp.Evt == "ERROR" {
		return fmt.Errorf("%w: %s", ErrRejected, resp.Data)
	}
	return nil
}

// Close closes the connection; Discord then clears the activity.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn, c.responses = nil, nil
	return err
}

func (c *Client) connect(ctx context.Context) error {
	dir := c.SocketDir
	if dir == "" {
		dir = os.TempDir()
	}
	var errs []error
	var d net.Dialer
	for i := range socketCount {
		path := filepath.Join(dir, fmt.Sprintf("discord-ipc-%d", i))
		if _, err := os.Stat(path); err != nil {
			continue
		}
		conn, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := handshake(conn, c.ClientID); err != nil {
			conn.Close()
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		c.conn, c.responses = conn, make(chan response, 4)
		go c.read(conn, c.responses)
		return nil
	}
	return errors.Join(append([]error{ErrNotRunning}, errs...)...)
}

type response struct {
	Evt     string          `json:"evt"`
	Nonce   string          `json:"nonce"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// handshake identifies the client; Discord answers with READY.
func handshake(conn net.Conn, clientID string) error {
	if err := conn.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	if err := writeFrame(conn, opHandshake, map[string]any{"v": 1, "client_id": clientID}); err != nil {
		return err
	}
	op, body, err := readFrame(conn)
	if err != nil {
		return err
	}
	resp, err := decodeResponse(op, body)
	if err != nil {
		return err
	}
	if resp.Evt == "ERROR" {
		return fmt.Errorf("handshake rejected: %s", resp.Data)
	}
	return conn.SetDeadline(time.Time{})
}

// read passes responses from conn to responses until conn fails. If Discord
// closed it, rather than the client, it reports Lost.
func (c *Client) read(conn net.Conn, responses chan<- response) {
	for {
		op, body, err := readFrame(conn)
		if err == nil {
			var resp response
			if resp, err = decodeResponse(op, body); err == nil {
				select {
				case responses <- resp:
				default: // nobody waits for it: a late response to a timed-out command
				}
				continue
			}
		}
		close(responses) // before locking: a command may hold the lock waiting
		c.mu.Lock()
		if c.conn == conn {
			c.closeLocked()
			select {
			case c.lostLocked() <- struct{}{}:
			default:
			}
		}
		c.mu.Unlock()
		return
	}
}

// command sends payload and waits for the response carrying its nonce.
// Responses to earlier commands that timed out are skipped.
func (c *Client) command(ctx context.Context, payload any, nonce string) (response, error) {
	if err := c.conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return response{}, err
	}
	if err := writeFrame(c.conn, opFrame, payload); err != nil {
		return response{}, err
	}
	timeout := time.NewTimer(ioTimeout)
	defer timeout.Stop()
	for {
		select {
		case resp, ok := <-c.responses:
			if !ok {
				return response{}, errors.New("connection closed")
			}
			if resp.Nonce == nonce {
				return resp, nil
			}
		case <-timeout.C:
			return response{}, errors.New("no response")
		case <-ctx.Done():
			return response{}, ctx.Err()
		}
	}
}

func decodeResponse(op uint32, body []byte) (response, error) {
	var resp response
	if err := json.Unmarshal(body, &resp); err != nil {
		return response{}, fmt.Errorf("decode response: %w", err)
	}
	if op == opClose {
		return response{}, fmt.Errorf("closed by Discord: %s", resp.Message)
	}
	return resp, nil
}

// writeFrame writes op and length as little-endian uint32s, then JSON.
func writeFrame(w io.Writer, op uint32, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := make([]byte, 8, 8+len(body))
	binary.LittleEndian.PutUint32(frame[0:], op)
	binary.LittleEndian.PutUint32(frame[4:], uint32(len(body)))
	_, err = w.Write(append(frame, body...))
	return err
}

func readFrame(r io.Reader) (op uint32, body []byte, err error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	op = binary.LittleEndian.Uint32(header[0:])
	body = make([]byte, binary.LittleEndian.Uint32(header[4:]))
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return op, body, nil
}

func nonce() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error
	return hex.EncodeToString(b[:])
}
