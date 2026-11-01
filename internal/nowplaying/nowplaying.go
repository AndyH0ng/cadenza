// Package nowplaying reads the system Now Playing state through
// mediaremote-adapter (https://github.com/ungive/mediaremote-adapter).
//
// Since macOS 15.4 only entitled binaries may use MediaRemote, so the adapter
// runs its framework inside /usr/bin/perl and streams JSON lines to stdout.
package nowplaying

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info is one Now Playing snapshot. Zero values mean "not reported".
type Info struct {
	BundleID  string    `json:"bundleIdentifier"`
	TrackID   ID        `json:"uniqueIdentifier"`
	Title     string    `json:"title"`
	Artist    string    `json:"artist"`
	Album     string    `json:"album"`
	Composer  string    `json:"composer"`
	Playing   bool      `json:"playing"`
	Duration  float64   `json:"duration"`     // seconds
	Elapsed   float64   `json:"elapsedTime"`  // seconds, as of Timestamp
	Rate      float64   `json:"playbackRate"` // 0 while paused or just resumed
	Timestamp Timestamp `json:"timestamp"`
}

// ID is a player-specific track identifier. For Apple Music it is the
// catalog ID. The adapter reports it as a number or a string.
type ID string

func (id *ID) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*id = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*id = ID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = ID(n.String())
	return nil
}

// Timestamp is an RFC 3339 time; malformed values decode to the zero time.
type Timestamp struct{ time.Time }

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		parsed = time.Time{}
	}
	t.Time = parsed
	return nil
}

// Adapter runs the mediaremote-adapter Perl script.
type Adapter struct {
	Dir  string // checkout of mediaremote-adapter with a build/ directory
	Perl string // defaults to /usr/bin/perl, the entitled system binary
}

func (a Adapter) script() string { return filepath.Join(a.Dir, "bin", "mediaremote-adapter.pl") }
func (a Adapter) framework() string {
	return filepath.Join(a.Dir, "build", "MediaRemoteAdapter.framework")
}

// Check reports whether the script and the built framework exist.
func (a Adapter) Check() error {
	for _, p := range []string{a.script(), a.framework()} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("nowplaying: adapter not ready: %w", err)
		}
	}
	return nil
}

// Stream calls fn with a full snapshot on every change until the adapter
// exits or ctx is done.
func (a Adapter) Stream(ctx context.Context, fn func(Info)) error {
	perl := a.Perl
	if perl == "" {
		perl = "/usr/bin/perl"
	}
	procCtx, kill := context.WithCancel(ctx)
	defer kill()
	cmd := exec.CommandContext(procCtx, perl, a.script(), a.framework(), "stream", "--no-diff", "--no-artwork", "--debounce=300")
	stderr := &tail{max: stderrTail}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nowplaying: start adapter: %w", err)
	}
	decodeErr := Decode(stdout, fn)
	if decodeErr != nil {
		// Nobody reads the pipe any more: the adapter would block writing to
		// it and Wait would never return.
		kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if decodeErr != nil {
		return decodeErr // waitErr only reports the kill
	}
	if msg := stderr.String(); waitErr != nil && msg != "" {
		return fmt.Errorf("nowplaying: adapter: %w: %s", waitErr, msg)
	}
	return waitErr
}

// stderrTail bounds how much of the adapter's stderr is kept for errors.
const stderrTail = 4 << 10

// tail keeps the last max bytes written to it. The adapter runs for as long
// as the program does, so its stderr is not kept in full.
type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

// Decode reads adapter messages from r and calls fn for each data payload.
// Stream asks for no artwork, but other adapter output can embed it in
// megabyte-long lines, so it decodes the stream instead of scanning lines.
func Decode(r io.Reader, fn func(Info)) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("nowplaying: decode: %w", err)
		}
		if msg.Type != "data" {
			continue
		}
		var info Info
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &info); err != nil {
				return fmt.Errorf("nowplaying: decode payload: %w", err)
			}
		}
		fn(info)
	}
}
