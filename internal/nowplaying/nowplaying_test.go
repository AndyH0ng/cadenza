package nowplaying

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Recorded from the adapter while pausing and resuming Apple Music.
const stream = `{"type":"data","diff":false,"payload":{}}
{"type":"data","diff":false,"payload":{"composer":"볼프강 아마데우스 모차르트","title":"Piano Concerto No. 22 in E-Flat Major, K. 482: II. Andante","duration":581.618,"bundleIdentifier":"com.apple.Music","uniqueIdentifier":1866870772,"elapsedTime":129.38,"playing":true,"timestamp":"2026-10-16T10:41:47Z","artist":"얀 리시에츠키","album":"Mozart: Piano Concertos Nos. 9 & 22","playbackRate":1,"artworkData":"/9j/4AAQSkZJRgABAQAAkACQAAD"}}
{"type":"error","message":"ignored"}
{"type":"data","diff":false,"payload":{"title":"x","uniqueIdentifier":"abc","timestamp":"not a time","playing":false,"playbackRate":0}}
`

func TestDecode(t *testing.T) {
	var got []Info
	if err := Decode(strings.NewReader(stream), func(i Info) { got = append(got, i) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d snapshots, want 3 (non-data messages skipped)", len(got))
	}
	if got[0] != (Info{}) {
		t.Errorf("empty payload = %+v, want zero Info", got[0])
	}

	m := got[1]
	want := Info{
		BundleID: "com.apple.Music", TrackID: "1866870772",
		Title: "Piano Concerto No. 22 in E-Flat Major, K. 482: II. Andante", Artist: "얀 리시에츠키",
		Album: "Mozart: Piano Concertos Nos. 9 & 22", Composer: "볼프강 아마데우스 모차르트",
		Playing: true, Duration: 581.618, Elapsed: 129.38, Rate: 1,
		Timestamp: Timestamp{time.Date(2026, 10, 16, 10, 41, 47, 0, time.UTC)},
	}
	if !m.Timestamp.Equal(want.Timestamp.Time) {
		t.Errorf("timestamp = %v, want %v", m.Timestamp, want.Timestamp)
	}
	m.Timestamp, want.Timestamp = Timestamp{}, Timestamp{}
	if m != want {
		t.Errorf("got  %+v\nwant %+v", m, want)
	}

	if got[2].TrackID != "abc" {
		t.Errorf("string ID = %q", got[2].TrackID)
	}
	if !got[2].Timestamp.IsZero() {
		t.Errorf("malformed timestamp = %v, want zero", got[2].Timestamp)
	}
}

func TestDecodeLargeLine(t *testing.T) {
	// Artwork makes lines far longer than bufio.Scanner's 64 KiB default.
	line := `{"type":"data","payload":{"title":"big","artworkData":"` + strings.Repeat("A", 4<<20) + `"}}`
	var titles []string
	if err := Decode(strings.NewReader(line), func(i Info) { titles = append(titles, i.Title) }); err != nil {
		t.Fatal(err)
	}
	if len(titles) != 1 || titles[0] != "big" {
		t.Errorf("titles = %q", titles)
	}
}

func TestDecodeMalformed(t *testing.T) {
	if err := Decode(strings.NewReader(`{"type":"data"`), func(Info) {}); err == nil {
		t.Error("want error for truncated JSON")
	}
}

func TestCheck(t *testing.T) {
	if err := (Adapter{Dir: t.TempDir()}).Check(); err == nil {
		t.Error("want error for a directory without the adapter")
	}
}

// fakeAdapter returns an Adapter whose "perl" is a shell script that ignores
// its arguments, prints out, then blocks if asked.
func fakeAdapter(t *testing.T, out string, block bool) Adapter {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + out + "EOF\n"
	if block {
		script += "exec sleep 60\n"
	}
	perl := filepath.Join(dir, "perl")
	if err := os.WriteFile(perl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Adapter{Dir: dir, Perl: perl}
}

func TestStream(t *testing.T) {
	a := fakeAdapter(t, stream, false)
	var n int
	if err := a.Stream(context.Background(), func(Info) { n++ }); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("got %d snapshots, want 3", n)
	}
}

func TestStreamMalformedStops(t *testing.T) {
	// The adapter keeps running after the bad line; Stream must not wait for it.
	a := fakeAdapter(t, "not json\n", true)
	done := make(chan error)
	go func() { done <- a.Stream(context.Background(), func(Info) {}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "decode") {
			t.Errorf("err = %v, want a decode error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream hung after malformed output")
	}
}

func TestStreamCancel(t *testing.T) {
	a := fakeAdapter(t, stream, true)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan struct{}, 10)
	done := make(chan error)
	go func() { done <- a.Stream(ctx, func(Info) { got <- struct{}{} }) }()
	<-got
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream did not stop after cancel")
	}
}

func TestStreamReportsStderr(t *testing.T) {
	dir := t.TempDir()
	perl := filepath.Join(dir, "perl")
	script := "#!/bin/sh\necho 'cannot load framework' >&2\nexit 1\n"
	if err := os.WriteFile(perl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Adapter{Dir: dir, Perl: perl}.Stream(context.Background(), func(Info) {})
	if err == nil || !strings.Contains(err.Error(), "cannot load framework") {
		t.Errorf("err = %v, want the adapter's stderr", err)
	}
}

func TestTail(t *testing.T) {
	tl := &tail{max: 4}
	tl.Write([]byte("abc"))
	tl.Write([]byte("def"))
	if got := tl.String(); got != "cdef" {
		t.Errorf("tail = %q, want the last 4 bytes", got)
	}
}
