package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDiscord serves one connection on dir/discord-ipc-0 and sends every
// received frame to the returned channel.
func fakeDiscord(t *testing.T, dir string, reply func(op uint32, body map[string]any) (uint32, any)) <-chan map[string]any {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, "discord-ipc-0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	frames := make(chan map[string]any, 10)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			var m map[string]any
			json.Unmarshal(body, &m)
			m["_op"] = float64(op)
			frames <- m
			rop, resp := reply(op, m)
			if err := writeFrame(conn, rop, resp); err != nil {
				return
			}
		}
	}()
	return frames
}

// socketDir returns a short directory: Unix socket paths are limited to 104
// bytes on macOS, and t.TempDir() under $TMPDIR can exceed that.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func okReply(op uint32, m map[string]any) (uint32, any) {
	if op == opHandshake {
		return opFrame, map[string]any{"evt": "READY", "data": map[string]any{"user": map[string]any{"username": "tester"}}}
	}
	return opFrame, map[string]any{"evt": nil, "cmd": "SET_ACTIVITY", "nonce": m["nonce"]}
}

func TestSetActivity(t *testing.T) {
	dir := socketDir(t)
	frames := fakeDiscord(t, dir, okReply)
	c := &Client{ClientID: "123", SocketDir: dir}
	defer c.Close()

	a := &Activity{Type: ActivityTypeListening, StatusDisplayType: StatusDisplayState, Details: "work", State: "composer",
		Timestamps: &Timestamps{Start: 1, End: 2}}
	if err := c.SetActivity(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	hs := <-frames
	if hs["_op"] != float64(opHandshake) || hs["client_id"] != "123" || hs["v"] != float64(1) {
		t.Errorf("handshake = %v", hs)
	}
	f := <-frames
	if f["_op"] != float64(opFrame) || f["cmd"] != "SET_ACTIVITY" || f["nonce"] == "" {
		t.Errorf("frame = %v", f)
	}
	args := f["args"].(map[string]any)
	if args["pid"] != float64(os.Getpid()) {
		t.Errorf("pid = %v", args["pid"])
	}
	act := args["activity"].(map[string]any)
	if act["type"] != float64(2) || act["status_display_type"] != float64(1) || act["details"] != "work" ||
		act["state"] != "composer" || act["assets"] != nil {
		t.Errorf("activity = %v", act)
	}

	// nil clears the activity on the same connection.
	if err := c.SetActivity(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if f := <-frames; f["args"].(map[string]any)["activity"] != nil {
		t.Errorf("clear sent activity %v", f["args"])
	}
}

func TestSetActivityNotRunning(t *testing.T) {
	c := &Client{ClientID: "123", SocketDir: socketDir(t)}
	if err := c.SetActivity(context.Background(), nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

func TestSetActivityRejected(t *testing.T) {
	dir := socketDir(t)
	fakeDiscord(t, dir, func(op uint32, m map[string]any) (uint32, any) {
		if op == opHandshake {
			return okReply(op, m)
		}
		return opFrame, map[string]any{"evt": "ERROR", "nonce": m["nonce"], "data": map[string]any{"message": "bad"}}
	})
	c := &Client{ClientID: "123", SocketDir: dir}
	defer c.Close()
	if err := c.SetActivity(context.Background(), &Activity{}); !errors.Is(err, ErrRejected) {
		t.Errorf("err = %v, want ErrRejected", err)
	}
}

func TestHandshakeClosed(t *testing.T) {
	dir := socketDir(t)
	fakeDiscord(t, dir, func(uint32, map[string]any) (uint32, any) {
		return opClose, map[string]any{"code": 4000, "message": "Invalid Client ID"}
	})
	c := &Client{ClientID: "bad", SocketDir: dir}
	if err := c.SetActivity(context.Background(), nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

func TestLostWhenDiscordQuits(t *testing.T) {
	dir := socketDir(t)
	ln, err := net.Listen("unix", filepath.Join(dir, "discord-ipc-0"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	quit := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 2; i++ { // handshake, then one command
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			var m map[string]any
			json.Unmarshal(body, &m)
			rop, resp := okReply(op, m)
			writeFrame(conn, rop, resp)
		}
		<-quit
	}()

	c := &Client{ClientID: "123", SocketDir: dir}
	defer c.Close()
	if err := c.SetActivity(context.Background(), &Activity{Details: "a"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Lost():
		t.Fatal("lost while connected")
	case <-time.After(50 * time.Millisecond):
	}
	close(quit) // Discord quits, removing its socket
	ln.Close()
	select {
	case <-c.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("no Lost after Discord closed the connection")
	}
	if err := c.SetActivity(context.Background(), nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("after quit err = %v, want ErrNotRunning", err)
	}
}

func TestCloseIsNotLost(t *testing.T) {
	dir := socketDir(t)
	fakeDiscord(t, dir, okReply)
	c := &Client{ClientID: "123", SocketDir: dir}
	if err := c.SetActivity(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case <-c.Lost():
		t.Error("Close reported Lost")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStaleResponseSkipped(t *testing.T) {
	dir := socketDir(t)
	fakeDiscord(t, dir, func(op uint32, m map[string]any) (uint32, any) {
		if op == opHandshake {
			return okReply(op, m)
		}
		// A response to some earlier command, then none to this one: the
		// client must not take the former for the latter.
		return opFrame, map[string]any{"evt": "ERROR", "nonce": "stale"}
	})
	c := &Client{ClientID: "123", SocketDir: dir}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.SetActivity(ctx, nil); err == nil || errors.Is(err, ErrRejected) {
		t.Errorf("err = %v, want a timeout, not the stale rejection", err)
	}
}
