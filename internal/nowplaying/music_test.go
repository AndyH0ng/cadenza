package nowplaying

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMusic(t *testing.T) {
	now := time.Date(2026, 10, 19, 6, 20, 0, 0, time.UTC)
	got, err := parseMusic("playing\x1fPiano Concerto No. 3\x1f임윤찬\x1fRachmaninoff: Piano Concerto No. 3\x1f세르게이 라흐마니노프\x1f1019.760009765625\x1f333,5\x1f4CE1C4511AF097A7", now)
	if err != nil {
		t.Fatal(err)
	}
	want := Info{BundleID: MusicBundleID, TrackID: "5539924869265790887", Title: "Piano Concerto No. 3", Artist: "임윤찬",
		Album: "Rachmaninoff: Piano Concerto No. 3", Composer: "세르게이 라흐마니노프", Playing: true,
		Duration: 1019.760009765625, Elapsed: 333.5, Rate: 1, Timestamp: Timestamp{now}}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}

	// Library IDs above 2^63 are negative in Now Playing.
	paused, err := parseMusic("paused\x1fx\x1f\x1f\x1f\x1f10\x1f1\x1fC8CF1A7CF2D7957B", now)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Playing || paused.Rate != 0 || paused.TrackID != "-3976930821992376965" {
		t.Errorf("paused = %+v", paused)
	}

	if stopped, err := parseMusic("stopped", now); err != nil || stopped != (Info{}) {
		t.Errorf("stopped = %+v, %v", stopped, err)
	}
	if _, err := parseMusic("garbage", now); err == nil {
		t.Error("want error for malformed output")
	}
}

func fakeOsascript(t *testing.T, script string) MusicApp {
	t.Helper()
	path := filepath.Join(t.TempDir(), "osascript")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return MusicApp{Osascript: path}
}

func TestMusicQuery(t *testing.T) {
	m := fakeOsascript(t, `printf 'playing\037T\037A\037B\037C\0372\0371\0370000000000000001\n'`)
	got, err := m.Query(context.Background())
	if err != nil || got.Title != "T" || got.TrackID != "1" || !got.Playing {
		t.Errorf("got %+v, %v", got, err)
	}

	denied := fakeOsascript(t, "echo 'execution error: Not authorized to send Apple events to Music. (-1743)' >&2; exit 1")
	if _, err := denied.Query(context.Background()); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("err = %v, want ErrNotAuthorized", err)
	}
}
