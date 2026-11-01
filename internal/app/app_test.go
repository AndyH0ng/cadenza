package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndyH0ng/cadenza/internal/discord"
	"github.com/AndyH0ng/cadenza/internal/metadata"
	"github.com/AndyH0ng/cadenza/internal/nowplaying"
	"github.com/AndyH0ng/cadenza/internal/presence"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeCatalog struct {
	classical  map[string]*metadata.ClassicalTrack
	libraryIDs map[string]string
	artwork    string // from iTunes
	song       *metadata.Song
	err        error
}

func (f fakeCatalog) Classical(_ context.Context, id string) (*metadata.ClassicalTrack, error) {
	return f.classical[id], f.err
}

// CatalogID resolves library IDs (negative) through the libraryIDs map.
func (f fakeCatalog) CatalogID(_ context.Context, q metadata.Query) (string, error) {
	if id, ok := f.libraryIDs[q.TrackID]; ok {
		return id, nil
	}
	return q.TrackID, nil
}

func (f fakeCatalog) Artwork(context.Context, string) (string, error) {
	return f.artwork, f.err
}

func (fakeCatalog) SongURL(id string) string { return "https://music/" + id }

func (f fakeCatalog) Song(context.Context, string) (*metadata.Song, error) {
	return f.song, f.err
}

var mozart = &metadata.ClassicalTrack{
	Work:          "피아노 협주곡 22번 내림마장조",
	Catalog:       "K. 482, KV 482",
	Movement:      "II. Andante",
	LocalMovement: "II. Andante",
	Composer:      "볼프강 아마데우스 모차르트",
	Artist:        "밤베르크 교향악단",
	Artwork:       "https://classical/cover.jpg",
	URL:           "https://classical/track",
	ComposerImage: "https://classical/mozart.jpg",
	ComposerURL:   "https://classical/artist/12374",
}

// activity returns what is shown for info once its lookups are done.
func (a *App) activity(ctx context.Context, info nowplaying.Info) *discord.Activity {
	if !a.shows(info) {
		return nil
	}
	t, _ := a.track(ctx, info)
	return presence.Build(t, a.Config.Presence, a.Now())
}

func music(id, title string, playing bool) nowplaying.Info {
	return nowplaying.Info{BundleID: "com.apple.Music", TrackID: nowplaying.ID(id), Title: title,
		Artist: "Artist", Composer: "Songwriter", Playing: playing, Duration: 200, Rate: 1}
}

func TestActivity(t *testing.T) {
	nocturne := &metadata.ClassicalTrack{
		Work:     "야상곡 1번 내림나단조",
		Catalog:  "B. 54/1, Op. 9/1",
		Movement: "Nocturne No. 1 in B-Flat Minor, Op. 9 No. 1", // Apple repeats the track title
		Composer: "프레데리크 쇼팽",
		Artist:   "마리아 주앙 피르스",
	}
	cat := fakeCatalog{
		classical: map[string]*metadata.ClassicalTrack{"1": mozart, "3": nocturne, "5": {
			Work: "12개의 연습곡", Movement: `No. 1 in A-Flat Major "Aeolian Harp"`,
			LocalMovement: `1번 내림가장조 "Aeolian Harp"`, Composer: "프레데리크 쇼팽", Artist: "얀 리시에츠키",
		}, "4": {
			Work: "사랑의 꿈", Catalog: "S. 541", Movement: "Liebestraum No. 3 in A-Flat Major, S. 541 No. 3",
			Composer: "프란츠 리스트", Artist: "랑 랑",
		}},
		libraryIDs: map[string]string{"-397": "1"},
		artwork:    "https://itunes/cover.jpg",
	}

	tests := []struct {
		name         string
		cfg          func(*Config)
		catalog      Catalog
		info         nowplaying.Info
		wantNil      bool
		third        string // checked when set
		details      string
		state, image string
	}{
		{name: "classical is localized", info: music("1", "Piano Concerto…", true),
			details: "피아노 협주곡 22번 내림마장조: II. Andante", state: "볼프강 아마데우스 모차르트", third: "밤베르크 교향악단", image: "https://classical/cover.jpg"},
		{name: "single-movement work shows only the work", info: music("3", "Nocturne No. 1 in B-Flat Minor, Op. 9 No. 1", true),
			details: "야상곡 1번 내림나단조", state: "프레데리크 쇼팽", third: "마리아 주앙 피르스", image: "https://itunes/cover.jpg"},
		{name: "piece number from the title", info: music("4", "Liebestraum No. 3 in A-Flat Major, S. 541 No. 3", true),
			details: "사랑의 꿈 3번", state: "프란츠 리스트", third: "랑 랑", image: "https://itunes/cover.jpg"},
		{name: "localized movement is shown", info: music("5", `12 Études, Op. 25: No. 1 in A-Flat Major "Aeolian Harp"`, true),
			details: `12개의 연습곡: 1번 내림가장조 "Aeolian Harp"`, state: "프레데리크 쇼팽", third: "얀 리시에츠키", image: "https://itunes/cover.jpg"},
		{name: "library ID is resolved", info: music("-397", "Piano Concerto…", true),
			details: "피아노 협주곡 22번 내림마장조: II. Andante", state: "볼프강 아마데우스 모차르트", image: "https://classical/cover.jpg"},
		{name: "unresolved library ID has no artwork lookup", catalog: fakeCatalog{libraryIDs: map[string]string{"-1": ""}, artwork: "https://wrong"},
			info: music("-1", "Ditto", true), details: "Ditto", state: "Artist"},
		{name: "pop hides the songwriter composer", info: music("2", "Ditto", true),
			details: "Ditto", state: "Artist", image: "https://itunes/cover.jpg"},
		{name: "pop cover from the song lookup", catalog: fakeCatalog{song: &metadata.Song{Artwork: "https://music/cover.jpg"}, artwork: "https://itunes/cover.jpg"},
			info: music("2", "Ditto", true), details: "Ditto", state: "Artist", image: "https://music/cover.jpg"},
		{name: "composer kept when not classical-only", cfg: func(c *Config) { c.ClassicalOnlyComposer = false },
			info: music("2", "Ditto", true), details: "Ditto", state: "Songwriter", image: "https://itunes/cover.jpg"},
		{name: "lookup errors fall back to Now Playing", catalog: fakeCatalog{err: errors.New("down")},
			info: music("1", "Piano Concerto…", true), details: "Piano Concerto…", state: "Artist"},
		{name: "other players are ignored", info: nowplaying.Info{BundleID: "com.spotify.client", Title: "x", Playing: true}, wantNil: true},
		{name: "empty Now Playing clears", info: nowplaying.Info{}, wantNil: true},
		{name: "paused is shown", info: music("2", "Ditto", false), details: "Ditto", state: "Artist", image: "https://itunes/cover.jpg"},
		{name: "paused clears in clear mode", cfg: func(c *Config) { c.Paused = PausedClear }, info: music("2", "Ditto", false), wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{Config: DefaultConfig(), Catalog: cat, Log: discardLog, Now: time.Now}
			if tt.cfg != nil {
				tt.cfg(&a.Config)
			}
			if tt.catalog != nil {
				a.Catalog = tt.catalog
			}
			got := a.activity(context.Background(), tt.info)
			if tt.wantNil {
				if got != nil {
					t.Errorf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil")
			}
			var image string
			if got.Assets != nil {
				image = got.Assets.LargeImage
			}
			if tt.third != "" && (got.Assets == nil || got.Assets.LargeText != tt.third) {
				t.Errorf("assets = %+v, want third line %q", got.Assets, tt.third)
			}
			if got.Details != tt.details || got.State != tt.state || image != tt.image {
				t.Errorf("got %q / %q / %q\nwant %q / %q / %q", got.Details, got.State, image, tt.details, tt.state, tt.image)
			}
		})
	}
}

func TestActivityButton(t *testing.T) {
	cat := fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}, libraryIDs: map[string]string{"-1": ""}}
	tests := []struct {
		name, id string
		want     []discord.Button
	}{
		{"classical links to Classical", "1", []discord.Button{{Label: "Apple Music에서 듣기", URL: "https://classical/track"}}},
		{"other tracks link to Apple Music", "2", []discord.Button{{Label: "Apple Music에서 듣기", URL: "https://music/2"}}},
		{"unresolved library track has no button", "-1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{Config: DefaultConfig(), Catalog: cat, Log: discardLog, Now: time.Now}
			got := a.activity(context.Background(), music(tt.id, "Title", true))
			if got == nil || !slices.Equal(got.Buttons, tt.want) {
				t.Errorf("buttons = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestActivityPortrait(t *testing.T) {
	newJeans := &metadata.Artist{Name: "뉴진스", Image: "https://music/newjeans.jpg", URL: "https://music/artist/1635469693"}
	cat := fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}, artwork: "https://itunes/cover.jpg", song: &metadata.Song{Artist: newJeans}}
	badge := func(cfg func(*Config), id string) discord.Assets {
		t.Helper()
		a := &App{Config: DefaultConfig(), Catalog: cat, Log: discardLog, Now: time.Now}
		if cfg != nil {
			cfg(&a.Config)
		}
		got := a.activity(context.Background(), music(id, "Title", true))
		if got == nil || got.Assets == nil {
			t.Fatalf("got %+v, want assets", got)
		}
		return discord.Assets{SmallImage: got.Assets.SmallImage, SmallText: got.Assets.SmallText, SmallURL: got.Assets.SmallURL}
	}

	if got, want := badge(nil, "1"), (discord.Assets{SmallImage: mozart.ComposerImage, SmallText: mozart.Composer, SmallURL: mozart.ComposerURL}); got != want {
		t.Errorf("classical badge = %+v, want the composer %+v", got, want)
	}
	if got, want := badge(nil, "2"), (discord.Assets{SmallImage: newJeans.Image, SmallText: newJeans.Name, SmallURL: newJeans.URL}); got != want {
		t.Errorf("pop badge = %+v, want the artist %+v", got, want)
	}
	// The songwriter shown as composer must not caption the artist's photo.
	if got := badge(func(c *Config) { c.ClassicalOnlyComposer = false }, "2"); got.SmallText != newJeans.Name {
		t.Errorf("pop badge text = %q, want %q", got.SmallText, newJeans.Name)
	}
	if got := badge(func(c *Config) { c.Presence.ShowPortrait = false }, "2"); got != (discord.Assets{}) {
		t.Errorf("disabled badge = %+v, want none", got)
	}
}

// hangingCatalog never answers until ctx is done, like an unreachable API.
type hangingCatalog struct{}

func (hangingCatalog) Classical(ctx context.Context, _ string) (*metadata.ClassicalTrack, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (hangingCatalog) CatalogID(ctx context.Context, _ metadata.Query) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (hangingCatalog) Artwork(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (hangingCatalog) SongURL(id string) string { return "" }

func (hangingCatalog) Song(ctx context.Context, _ string) (*metadata.Song, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestActivityLookupTimeout(t *testing.T) {
	a := &App{Config: DefaultConfig(), Catalog: hangingCatalog{}, Log: discardLog, Now: time.Now}
	a.Config.LookupTimeout = 50 * time.Millisecond
	start := time.Now()
	got := a.activity(context.Background(), music("1", "Piano Concerto…", true))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("activity took %v, want about %v", elapsed, a.Config.LookupTimeout)
	}
	if got == nil || got.Details != "Piano Concerto…" || got.State != "Artist" {
		t.Errorf("got %+v, want the Now Playing fallback", got)
	}
}

// recorder is a fake Discord that records what it was sent, with times.
type recorder struct {
	mu    sync.Mutex
	calls int // including failed ones
	sent  []*discord.Activity
	times []time.Time
	err   error
}

func (r *recorder) SetActivity(_ context.Context, a *discord.Activity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, a)
	r.times = append(r.times, time.Now())
	return nil
}

func (r *recorder) snapshot() ([]*discord.Activity, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*discord.Activity(nil), r.sent...), append([]time.Time(nil), r.times...)
}

func (r *recorder) setErr(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func testSenderConfig() Config {
	cfg := DefaultConfig()
	cfg.MinUpdateInterval = 100 * time.Millisecond
	cfg.RetryInterval = time.Hour // no idle refreshes unless a test wants them
	return cfg
}

func startSender(t *testing.T, cfg Config, r *recorder) *sender {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := newSender(r, cfg, discardLog)
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

func act(details string) *discord.Activity { return &discord.Activity{Type: 2, Details: details} }

func waitFor(t *testing.T, r *recorder, n int) []*discord.Activity {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sent, _ := r.snapshot(); len(sent) >= n {
			return sent
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, _ := r.snapshot()
	t.Fatalf("got %d sends, want %d", len(sent), n)
	return nil
}

func TestSenderCoalescesAndRateLimits(t *testing.T) {
	cfg := testSenderConfig()
	r := &recorder{}
	s := startSender(t, cfg, r)

	s.want(act("a"))
	waitFor(t, r, 1)
	// Within the interval: only the last of these may be sent.
	s.want(act("b"))
	s.want(act("c"))
	s.want(act("d"))
	sent := waitFor(t, r, 2)
	time.Sleep(3 * cfg.MinUpdateInterval)

	sent, times := r.snapshot()
	if len(sent) != 2 || sent[0].Details != "a" || sent[1].Details != "d" {
		var got []string
		for _, a := range sent {
			got = append(got, a.Details)
		}
		t.Fatalf("sent %q, want [a d]", got)
	}
	if gap := times[1].Sub(times[0]); gap < cfg.MinUpdateInterval {
		t.Errorf("updates %v apart, want ≥ %v", gap, cfg.MinUpdateInterval)
	}
}

func TestSenderSkipsUnchanged(t *testing.T) {
	r := &recorder{}
	s := startSender(t, testSenderConfig(), r)

	s.want(nil) // already Discord's initial state
	s.want(act("a"))
	waitFor(t, r, 1)
	s.want(act("a"))
	time.Sleep(300 * time.Millisecond)
	if sent, _ := r.snapshot(); len(sent) != 1 {
		t.Errorf("sent %d times, want 1", len(sent))
	}
}

func TestSenderRetriesAfterFailure(t *testing.T) {
	cfg := testSenderConfig()
	cfg.RetryInterval = 50 * time.Millisecond
	r := &recorder{err: discord.ErrNotRunning}
	s := startSender(t, cfg, r)

	s.want(act("a"))
	time.Sleep(80 * time.Millisecond)
	r.setErr(nil) // Discord started
	if sent := waitFor(t, r, 1); sent[0].Details != "a" {
		t.Errorf("sent %q", sent[0].Details)
	}
}

func TestSenderDoesNotRetryRejected(t *testing.T) {
	cfg := testSenderConfig()
	cfg.RetryInterval = 30 * time.Millisecond
	r := &recorder{err: fmt.Errorf("%w: bad field", discord.ErrRejected)}
	s := startSender(t, cfg, r)

	s.want(act("a"))
	time.Sleep(300 * time.Millisecond) // many retry and refresh intervals
	r.mu.Lock()
	calls := r.calls
	r.mu.Unlock()
	if calls != 1 {
		t.Errorf("rejected activity sent %d times, want 1", calls)
	}

	r.setErr(nil)
	s.want(act("b")) // a change is sent again
	if sent := waitFor(t, r, 1); sent[0].Details != "b" {
		t.Errorf("sent %q, want b", sent[0].Details)
	}
}

// lostRecorder is a recorder that can report a lost connection.
type lostRecorder struct {
	*recorder
	lost chan struct{}
}

func (l lostRecorder) Lost() <-chan struct{} { return l.lost }

func TestSenderResendsAfterLost(t *testing.T) {
	cfg := testSenderConfig()
	cfg.RetryInterval = 30 * time.Millisecond
	r := lostRecorder{&recorder{}, make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	s := newSender(r, cfg, discardLog)
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	s.want(act("a"))
	waitFor(t, r.recorder, 1)
	time.Sleep(100 * time.Millisecond) // many retry intervals: no refreshes
	if sent, _ := r.snapshot(); len(sent) != 1 {
		t.Fatalf("sent %d times while connected, want 1", len(sent))
	}

	r.setErr(discord.ErrNotRunning) // Discord quit and is restarting
	r.lost <- struct{}{}
	time.Sleep(80 * time.Millisecond)
	r.setErr(nil)
	if sent := waitFor(t, r.recorder, 2); sent[1].Details != "a" {
		t.Errorf("resent %q, want a", sent[1].Details)
	}
}

func TestSenderIgnoresLostWhenCleared(t *testing.T) {
	r := lostRecorder{&recorder{}, make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	s := newSender(r, testSenderConfig(), discardLog)
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	s.want(nil)
	r.lost <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	r.mu.Lock()
	calls := r.calls
	r.mu.Unlock()
	if calls != 0 {
		t.Errorf("%d sends after losing a cleared status, want 0", calls)
	}
}

// fakeSource replays snapshots once, then blocks until ctx is done.
type fakeSource struct{ infos []nowplaying.Info }

func (f fakeSource) Stream(ctx context.Context, fn func(nowplaying.Info)) error {
	for _, i := range f.infos {
		fn(i)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestRun(t *testing.T) {
	cfg := testSenderConfig()
	r := &recorder{}
	a := &App{
		Config:   cfg,
		Source:   fakeSource{[]nowplaying.Info{music("1", "Piano Concerto…", true), music("1", "Piano Concerto…", false)}},
		Catalog:  fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}},
		Presence: r,
		Log:      discardLog,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Run(ctx) }()

	// The playing snapshot may or may not be sent before it is superseded;
	// the paused one must end up shown.
	deadline := time.Now().Add(2 * time.Second)
	var last *discord.Activity
	for time.Now().Before(deadline) {
		if sent, _ := r.snapshot(); len(sent) > 0 {
			if last = sent[len(sent)-1]; last.Assets != nil && last.Assets.SmallImage != "" {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if last == nil || last.Assets == nil || last.Assets.SmallImage == "" {
		t.Errorf("last sent %+v, want the paused activity", last)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

// queryRecorder is a Catalog that records the CatalogID query.
type queryRecorder struct {
	fakeCatalog
	got *metadata.Query
}

func (q queryRecorder) CatalogID(_ context.Context, query metadata.Query) (string, error) {
	*q.got = query
	return "9", nil
}

func TestAlbumReleaseType(t *testing.T) {
	var got metadata.Query
	a := &App{Config: DefaultConfig(), Catalog: queryRecorder{fakeCatalog{artwork: "https://cover"}, &got}, Log: discardLog, Now: time.Now}
	info := nowplaying.Info{BundleID: "com.apple.Music", TrackID: "4385621753610067973", Title: "로켓트",
		Artist: "잔나비", Album: "로켓트 - Single", Playing: true}

	act := a.activity(context.Background(), info)
	if act == nil || act.Assets == nil || act.Assets.LargeText != "로켓트" {
		t.Errorf("assets = %+v, want album %q", act.Assets, "로켓트")
	}
	if got.Album != "로켓트 - Single" {
		t.Errorf("search album = %q, want Apple's full name", got.Album)
	}
}

// slowCatalog answers Classical lookups only after release is closed.
type slowCatalog struct {
	fakeCatalog
	release chan struct{}
}

func (c slowCatalog) Classical(ctx context.Context, id string) (*metadata.ClassicalTrack, error) {
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.fakeCatalog.Classical(ctx, id)
}

func startRun(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestRunShowsNowPlayingWhileLookingUp(t *testing.T) {
	cfg := testSenderConfig()
	cfg.LookupGrace = 20 * time.Millisecond
	r := &recorder{}
	cat := slowCatalog{fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}}, make(chan struct{})}
	startRun(t, &App{Config: cfg, Source: fakeSource{[]nowplaying.Info{music("1", "Piano Concerto…", true)}},
		Catalog: cat, Presence: r, Log: discardLog})

	if sent := waitFor(t, r, 1); sent[0].Details != "Piano Concerto…" {
		t.Fatalf("first sent %q, want the Now Playing title", sent[0].Details)
	}
	close(cat.release)
	if sent := waitFor(t, r, 2); sent[1].Details != "피아노 협주곡 22번 내림마장조: II. Andante" {
		t.Errorf("then sent %q, want the localized work", sent[1].Details)
	}
}

func TestRunWaitsForQuickLookup(t *testing.T) {
	r := &recorder{}
	startRun(t, &App{Config: testSenderConfig(), Source: fakeSource{[]nowplaying.Info{music("1", "Piano Concerto…", true)}},
		Catalog: fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}}, Presence: r, Log: discardLog})

	waitFor(t, r, 1)
	time.Sleep(300 * time.Millisecond)
	sent, _ := r.snapshot()
	if len(sent) != 1 || sent[0].Details != "피아노 협주곡 22번 내림마장조: II. Andante" {
		t.Errorf("sent %d activities, first %q; want only the localized one", len(sent), sent[0].Details)
	}
}

func TestRunPlaybackChangeKeepsLookup(t *testing.T) {
	// Pausing a track whose lookup is pending must not show the Now Playing
	// fallback once the lookup is done.
	cfg := testSenderConfig()
	cfg.LookupGrace = 20 * time.Millisecond
	r := &recorder{}
	cat := slowCatalog{fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}}, make(chan struct{})}
	src := fakeSource{[]nowplaying.Info{music("1", "Piano Concerto…", true), music("1", "Piano Concerto…", false)}}
	startRun(t, &App{Config: cfg, Source: src, Catalog: cat, Presence: r, Log: discardLog})

	waitFor(t, r, 1)
	close(cat.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent, _ := r.snapshot()
		if last := sent[len(sent)-1]; last.Details == mozart.Work+": II. Andante" && last.Timestamps != nil &&
			last.Assets != nil && last.Assets.SmallImage == DefaultConfig().Presence.PausedImage {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, _ := r.snapshot()
	t.Errorf("last sent %+v, want the paused localized activity", sent[len(sent)-1])
}

// chanSource streams the snapshots sent on its channel.
type chanSource chan nowplaying.Info

func (c chanSource) Stream(ctx context.Context, fn func(nowplaying.Info)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case info := <-c:
			fn(info)
		}
	}
}

// fakeMusic answers Query with whatever was last set.
type fakeMusic struct {
	mu      sync.Mutex
	info    nowplaying.Info
	err     error
	queries int
}

func (m *fakeMusic) Query(context.Context) (nowplaying.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries++
	return m.info, m.err
}

func (m *fakeMusic) set(info nowplaying.Info, err error) {
	m.mu.Lock()
	m.info, m.err = info, err
	m.mu.Unlock()
}

func (m *fakeMusic) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queries
}

var safari = nowplaying.Info{BundleID: "com.apple.WebKit.GPU", Title: "Lecture", Playing: true, Duration: 2800, Rate: 1}

func musicTestApp(t *testing.T) (*App, chanSource, *fakeMusic, *recorder) {
	t.Helper()
	cfg := testSenderConfig()
	cfg.MinUpdateInterval = 10 * time.Millisecond
	cfg.MusicPollInterval = 20 * time.Millisecond
	src, m, r := make(chanSource), &fakeMusic{}, &recorder{}
	a := &App{Config: cfg, Source: src, Catalog: fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}},
		Presence: r, Music: m, Log: discardLog}
	startRun(t, a)
	return a, src, m, r
}

// eventually waits until the last sent activity satisfies ok.
func eventually(t *testing.T, r *recorder, what string, ok func(*discord.Activity) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sent, _ := r.snapshot(); len(sent) > 0 && ok(sent[len(sent)-1]) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, _ := r.snapshot()
	t.Fatalf("want %s; sent %d, last %+v", what, len(sent), sent)
}

func isPaused(a *discord.Activity) bool {
	return a != nil && a.Assets != nil && a.Assets.SmallImage == DefaultConfig().Presence.PausedImage
}

func TestMusicFollowedWhileAnotherAppPlays(t *testing.T) {
	_, src, m, r := musicTestApp(t)
	playing := music("1", "Piano Concerto…", true)
	src <- playing
	eventually(t, r, "the localized track", func(a *discord.Activity) bool { return a != nil && a.Details == mozart.Work+": II. Andante" })

	// Safari takes Now Playing; Music, asked directly, still plays the same
	// track, which it reports by its persistent ID.
	fromMusic := playing
	fromMusic.TrackID = "5539924869265790887"
	m.set(fromMusic, nil)
	src <- safari
	time.Sleep(100 * time.Millisecond) // several polls
	if m.count() == 0 {
		t.Fatal("Music was not queried")
	}
	sent, _ := r.snapshot()
	for _, a := range sent {
		if a == nil || a.Details != mozart.Work+": II. Andante" {
			t.Fatalf("sent %+v while Music kept playing", a)
		}
	}

	paused := fromMusic
	paused.Playing, paused.Rate = false, 0
	m.set(paused, nil) // paused behind Safari
	eventually(t, r, "the paused track", func(a *discord.Activity) bool { return isPaused(a) && a.Details == mozart.Work+": II. Andante" })

	m.set(nowplaying.Info{}, nil) // Music quit
	eventually(t, r, "cleared", func(a *discord.Activity) bool { return a == nil })
	n := m.count()
	time.Sleep(100 * time.Millisecond)
	if m.count() != n {
		t.Error("Music still polled after it stopped")
	}
}

func TestMusicBackInNowPlayingStopsPolling(t *testing.T) {
	_, src, m, r := musicTestApp(t)
	src <- music("1", "Piano Concerto…", true)
	waitFor(t, r, 1)
	m.set(music("1", "Piano Concerto…", true), nil)
	src <- safari
	time.Sleep(50 * time.Millisecond)
	src <- music("1", "Piano Concerto…", false) // Music paused, back in Now Playing
	eventually(t, r, "the paused track", isPaused)
	n := m.count()
	time.Sleep(100 * time.Millisecond)
	if m.count() != n {
		t.Error("Music still polled after Now Playing reported it")
	}
}

func TestOtherAppWithMusicStoppedQueriesOnce(t *testing.T) {
	_, src, m, r := musicTestApp(t)
	src <- safari
	time.Sleep(100 * time.Millisecond) // several poll intervals
	if m.count() != 1 {
		t.Errorf("Music queried %d times, want once: it is stopped", m.count())
	}
	if sent, _ := r.snapshot(); len(sent) != 0 {
		t.Errorf("sent %+v, want nothing", sent)
	}
}

func TestMusicBehindOtherAppAtStart(t *testing.T) {
	_, src, m, r := musicTestApp(t)
	m.set(music("1", "Piano Concerto…", true), nil)
	src <- safari // Safari already held Now Playing when the program started
	eventually(t, r, "the Music track", func(a *discord.Activity) bool { return a != nil && a.Details == mozart.Work+": II. Andante" })
}

func TestMusicNotAuthorizedClears(t *testing.T) {
	_, src, m, r := musicTestApp(t)
	src <- music("1", "Piano Concerto…", true)
	waitFor(t, r, 1)
	m.set(nowplaying.Info{}, nowplaying.ErrNotAuthorized)
	src <- safari
	eventually(t, r, "cleared", func(a *discord.Activity) bool { return a == nil })
}

// flakyCatalog fails Classical lookups until fails reaches 0.
type flakyCatalog struct {
	fakeCatalog
	mu    *sync.Mutex
	fails *int
}

func (c flakyCatalog) Classical(ctx context.Context, id string) (*metadata.ClassicalTrack, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *c.fails > 0 {
		*c.fails--
		return nil, errors.New("offline")
	}
	return c.fakeCatalog.Classical(ctx, id)
}

func TestRunRetriesFailedLookup(t *testing.T) {
	cfg := testSenderConfig()
	cfg.MinUpdateInterval = 10 * time.Millisecond
	cfg.LookupRetryInterval = 20 * time.Millisecond
	fails := 2
	cat := flakyCatalog{fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart}}, &sync.Mutex{}, &fails}
	r := &recorder{}
	startRun(t, &App{Config: cfg, Source: fakeSource{[]nowplaying.Info{music("1", "Piano Concerto…", true)}},
		Catalog: cat, Presence: r, Log: discardLog})

	if sent := waitFor(t, r, 1); sent[0].Details != "Piano Concerto…" {
		t.Fatalf("first sent %q, want the Now Playing fallback", sent[0].Details)
	}
	eventually(t, r, "the localized work after the API recovered", func(a *discord.Activity) bool {
		return a != nil && a.Details == mozart.Work+": II. Andante"
	})
}

func TestRunStopsRetryingAfterLimit(t *testing.T) {
	cfg := testSenderConfig()
	cfg.LookupRetryInterval = 5 * time.Millisecond
	cfg.LookupRetries = 3
	fails := 1000
	mu := &sync.Mutex{}
	cat := flakyCatalog{fakeCatalog{}, mu, &fails}
	startRun(t, &App{Config: cfg, Source: fakeSource{[]nowplaying.Info{music("1", "Piano Concerto…", true)}},
		Catalog: cat, Presence: &recorder{}, Log: discardLog})

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if tries := 1000 - fails; tries != 1+cfg.LookupRetries {
		t.Errorf("looked up %d times, want %d", tries, 1+cfg.LookupRetries)
	}
}

func TestMusicTransientErrorKeepsTrack(t *testing.T) {
	_, src, m, r := musicTestApp(t)
	src <- music("1", "Piano Concerto…", true)
	waitFor(t, r, 1)
	m.set(nowplaying.Info{}, errors.New("osascript timed out"))
	src <- safari
	time.Sleep(100 * time.Millisecond) // several failed polls
	sent, _ := r.snapshot()
	if last := sent[len(sent)-1]; last == nil {
		t.Fatal("cleared after a transient failure")
	}
	n := m.count()
	if n < 2 {
		t.Fatalf("Music queried %d times, want polling to continue", n)
	}

	paused := music("1", "Piano Concerto…", false)
	m.set(paused, nil) // osascript works again
	eventually(t, r, "the paused track", isPaused)
}

func TestAPIChangedLoggedOnce(t *testing.T) {
	var logs strings.Builder
	cat := &fakeCatalog{classical: map[string]*metadata.ClassicalTrack{"1": mozart},
		err: fmt.Errorf("%w: developer token: none in bundle", metadata.ErrAPIChanged)}
	a := &App{Config: DefaultConfig(), Catalog: cat, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: time.Now}
	for range 3 {
		a.activity(context.Background(), music("1", "Piano Concerto…", true))
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("%d errors logged, want 1:\n%s", n, logs.String())
	}

	cat.err = nil
	a.activity(context.Background(), music("1", "Piano Concerto…", true))
	if !strings.Contains(logs.String(), "work again") {
		t.Errorf("recovery not logged:\n%s", logs.String())
	}
}
