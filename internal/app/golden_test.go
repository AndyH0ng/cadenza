package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndyH0ng/cadenza/internal/metadata"
	"github.com/AndyH0ng/cadenza/internal/nowplaying"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden with the current output")

// goldenTrack is a crawled Apple Music Classical track: the Now Playing title
// and the raw fields of the Classical API (null when Apple has none).
type goldenTrack struct {
	Title                string  `json:"title"`
	WorkTitle            *string `json:"workTitle"`
	WorkSubtitle         *string `json:"workSubtitle"`
	MovementOrTrackTitle *string `json:"movementOrTrackTitle"`
}

// The golden tests render the title line of real Apple Music Classical
// tracks through the real metadata client and app, against a fake server
// that replays their crawled API fields, and compare the result with a
// golden file. After an intended change, review the diff of
// `go test ./internal/app -run Golden -update`.
//
// classical_sample.json holds a sample of every title rule and is checked
// in. classical_tracks.json, the full crawl, is kept locally; its test is
// skipped without it.

func TestGoldenSample(t *testing.T) {
	testGolden(t, "testdata/classical_sample.json", "testdata/classical_sample.golden")
}

func TestGoldenAll(t *testing.T) {
	if _, err := os.Stat("testdata/classical_tracks.json"); errors.Is(err, fs.ErrNotExist) {
		t.Skip("testdata/classical_tracks.json is not checked in")
	}
	testGolden(t, "testdata/classical_tracks.json", "testdata/classical_titles.golden")
}

func testGolden(t *testing.T, tracksFile, golden string) {
	t.Helper()
	var tracks map[string]goldenTrack
	b, err := os.ReadFile(tracksFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &tracks); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/classical/kr" {
			w.Write([]byte(`{"results":[]}`)) // iTunes artwork: none
			return
		}
		id := r.URL.Query().Get("ids")
		tr := tracks[id]
		json.NewEncoder(w).Encode(map[string]any{"trackMetadataMap": map[string]any{id: map[string]any{
			"workTitle": tr.WorkTitle, "workSubtitle": tr.WorkSubtitle, "movementOrTrackTitle": tr.MovementOrTrackTitle,
			"composerTitle": "composer", "displayArtists": "performers",
		}}})
	}))
	defer srv.Close()
	client := metadata.NewClient("kr", "ko-KR")
	client.HTTP = srv.Client()
	client.ClassicalBase = srv.URL + "/classical"
	client.ITunesBase = srv.URL
	a := &App{Config: DefaultConfig(), Catalog: client, Log: discardLog, Now: time.Now}

	ids := make([]string, 0, len(tracks))
	for id := range tracks {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(x, y string) int {
		nx, _ := strconv.ParseInt(x, 10, 64)
		ny, _ := strconv.ParseInt(y, 10, 64)
		return cmp.Compare(nx, ny)
	})
	var out strings.Builder
	out.WriteString("# id\tshown title\t<- Now Playing title | Classical work | movement\n")
	for _, id := range ids {
		tr := tracks[id]
		act := a.activity(context.Background(), nowplaying.Info{
			BundleID: "com.apple.Music", TrackID: nowplaying.ID(id), Title: tr.Title, Playing: true,
		})
		fmt.Fprintf(&out, "%s\t%s\t<- %s | %s | %s\n", id, act.Details, tr.Title, deref(tr.WorkTitle), deref(tr.MovementOrTrackTitle))
	}

	if *update {
		if err := os.WriteFile(golden, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if diff := diffLines(string(want), out.String()); diff != "" {
		t.Errorf("titles differ from %s (run with -update if intended):\n%s", golden, diff)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// diffLines lists changed lines of two outputs with the same line count, at
// most 50 of them.
func diffLines(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	if len(w) != len(g) {
		return fmt.Sprintf("line count %d, want %d", len(g), len(w))
	}
	var b strings.Builder
	n := 0
	for i := range w {
		if w[i] == g[i] {
			continue
		}
		if n++; n <= 50 {
			fmt.Fprintf(&b, "- %s\n+ %s\n", w[i], g[i])
		}
	}
	if n > 50 {
		fmt.Fprintf(&b, "… and %d more\n", n-50)
	}
	return b.String()
}
