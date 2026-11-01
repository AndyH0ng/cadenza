package presence

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AndyH0ng/cadenza/internal/discord"
)

var now = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func classical() Track {
	return Track{
		Title:         "Piano Concerto No. 22 in E-Flat Major, K. 482: II. Andante",
		Artist:        "밤베르크 교향악단, 만프레드 호네크, 얀 리시에츠키",
		Album:         "Mozart: Piano Concertos Nos. 9 & 22",
		Composer:      "볼프강 아마데우스 모차르트",
		Work:          "피아노 협주곡 22번 내림마장조",
		Catalog:       "K. 482, KV 482",
		Movement:      "II. Andante",
		ComposerShort: "W. A. 모차르트",
		Performers:    "밤베르크 교향악단, 만프레드 호네크, 얀 리시에츠키",
		Artwork:       "https://example.com/cover.jpg",
		Playing:       true,
		Duration:      580 * time.Second,
		Elapsed:       100 * time.Second,
		Rate:          1,
		At:            now.Add(-10 * time.Second),
	}
}

func TestBuildText(t *testing.T) {
	pop := Track{Title: "Ditto", Artist: "NewJeans", Album: "Ditto - Single", Artwork: "https://example.com/d.jpg", Playing: true}

	tests := []struct {
		name                  string
		track                 Track
		details, state, third string
	}{
		{"classical: work, composer, performers", classical(),
			"피아노 협주곡 22번 내림마장조: II. Andante", "볼프강 아마데우스 모차르트",
			"밤베르크 교향악단, 만프레드 호네크, 얀 리시에츠키"},
		{"pop: title, artist, album", pop, "Ditto", "NewJeans", "Ditto - Single"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Build(tt.track, DefaultConfig(), now)
			if a.Details != tt.details || a.State != tt.state {
				t.Errorf("got %q / %q, want %q / %q", a.Details, a.State, tt.details, tt.state)
			}
			if a.Type != discord.ActivityTypeListening || a.StatusDisplayType != discord.StatusDisplayState {
				t.Errorf("type/status_display_type = %d/%d", a.Type, a.StatusDisplayType)
			}
			if a.Assets == nil || a.Assets.LargeText != tt.third {
				t.Errorf("assets = %+v, want third line %q", a.Assets, tt.third)
			}
		})
	}
}

func TestBuildNoTitle(t *testing.T) {
	if a := Build(Track{Artist: "x", Playing: true}, DefaultConfig(), now); a != nil {
		t.Errorf("Build without any details = %+v, want nil", a)
	}
}

func TestBuildProgress(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Track)
		start, end time.Time
	}{
		{"anchored at the reported time", func(*Track) {},
			now.Add(-110 * time.Second), now.Add(470 * time.Second)},
		{"rate 0 after resume restarts from the paused position now", func(tr *Track) {
			tr.Rate = 0
			tr.At = now.Add(-time.Hour) // time of the pause
		}, now.Add(-100 * time.Second), now.Add(480 * time.Second)},
		{"unknown time anchors at now", func(tr *Track) { tr.At = time.Time{} },
			now.Add(-100 * time.Second), now.Add(480 * time.Second)},
		{"double speed halves wall time", func(tr *Track) { tr.Rate = 2 },
			now.Add(-60 * time.Second), now.Add(230 * time.Second)},
		{"elapsed past duration ends at the anchor", func(tr *Track) { tr.Elapsed = 600 * time.Second },
			now.Add(-610 * time.Second), now.Add(-10 * time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := classical()
			tt.mutate(&tr)
			ts := Build(tr, DefaultConfig(), now).Timestamps
			if ts == nil {
				t.Fatal("no timestamps")
			}
			if ts.Start != tt.start.UnixMilli() || ts.End != tt.end.UnixMilli() {
				t.Errorf("start/end = %v/%v, want %v/%v",
					time.UnixMilli(ts.Start).UTC(), time.UnixMilli(ts.End).UTC(), tt.start, tt.end)
			}
		})
	}
}

func TestBuildPaused(t *testing.T) {
	tr := classical()
	tr.Playing = false
	a := Build(tr, DefaultConfig(), now)
	if ts := a.Timestamps; ts == nil || ts.Start <= now.UnixMilli() || ts.End-ts.Start != tr.Duration.Milliseconds() {
		t.Errorf("paused timestamps = %+v, want a full-length bar starting in the future (0:00)", ts)
	}
	if b := Build(tr, DefaultConfig(), now.Add(time.Hour)); !Equal(a, b, 0) {
		t.Error("frozen paused activity changes over time")
	}
	if a.State != "볼프강 아마데우스 모차르트" || a.Assets.SmallImage != DefaultConfig().PausedImage || a.Assets.SmallText != "일시정지됨" ||
		a.Assets.LargeImage != tr.Artwork {
		t.Errorf("state = %q", a.State)
	}

	// Without a cover the badge image takes its place.
	tr.Artwork = ""
	if a := Build(tr, DefaultConfig(), now); a.Assets == nil || a.Assets.LargeImage != DefaultConfig().PausedImage || a.Assets.SmallImage != "" {
		t.Errorf("assets without cover = %+v", a.Assets)
	}

	// No badge and no frozen bar.
	cfg := DefaultConfig()
	cfg.PausedImage = ""
	cfg.FreezePausedProgress = false
	if a := Build(tr, cfg, now); a.Assets != nil || a.Timestamps != nil {
		t.Errorf("activity = %+v", a)
	}
}

func TestRender(t *testing.T) {
	fields := map[string]string{"title": "T", "artist": "A", "album": ""}
	tests := []struct {
		formats []string
		want    string
	}{
		{[]string{"{album}", "{title} — {artist}"}, "T — A"}, // empty value skips a format
		{[]string{"{unknown}", "{title}"}, "T "},             // unknown is empty; padded to 2
		{[]string{"literal only"}, ""},                       // needs a placeholder
		{nil, ""},
	}
	for _, tt := range tests {
		if got := Render(tt.formats, fields); got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.formats, got, tt.want)
		}
	}
}

func TestRenderTruncatesByCharacter(t *testing.T) {
	long := strings.Repeat("가", 200) // 3 bytes each: byte slicing would break UTF-8
	got := Render([]string{"{title}"}, map[string]string{"title": long})
	if n := utf8.RuneCountInString(got); n != discord.MaxTextLen || !utf8.ValidString(got) {
		t.Errorf("len = %d runes, valid = %v", n, utf8.ValidString(got))
	}
}

func TestEqual(t *testing.T) {
	base := func() *discord.Activity { return Build(classical(), DefaultConfig(), now) }
	shift := func(d time.Duration) *discord.Activity {
		a := base()
		a.Timestamps.Start += d.Milliseconds()
		a.Timestamps.End += d.Milliseconds()
		return a
	}
	paused := func() *discord.Activity {
		tr := classical()
		tr.Playing = false
		return Build(tr, DefaultConfig(), now)
	}
	otherCover := base()
	otherCover.Assets.LargeImage = "https://example.com/other.jpg"
	otherLink := base()
	otherLink.Buttons = []discord.Button{{Label: "x", URL: "https://example.com/other"}}

	tol := 2 * time.Second
	tests := []struct {
		name string
		a, b *discord.Activity
		want bool
	}{
		{"both nil", nil, nil, true},
		{"nil vs set", nil, base(), false},
		{"identical", base(), base(), true},
		{"jitter within tolerance", base(), shift(1500 * time.Millisecond), true},
		{"seek beyond tolerance", base(), shift(3 * time.Second), false},
		{"playing vs paused", base(), paused(), false},
		{"different cover", base(), otherCover, false},
		{"different button", base(), otherLink, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Equal(tt.a, tt.b, tol); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestButton(t *testing.T) {
	links := Track{Title: "t", ClassicalURL: "https://classical/x", MusicURL: "https://music/x", Playing: true}
	tests := []struct {
		name  string
		track Track
		cfg   func(*Config)
		want  []discord.Button
	}{
		{"classical wins", links, nil, []discord.Button{{Label: "Apple Music에서 듣기", URL: "https://classical/x"}}},
		{"music otherwise", Track{Title: "t", MusicURL: "https://music/x"}, nil, []discord.Button{{Label: "Apple Music에서 듣기", URL: "https://music/x"}}},
		{"no link", Track{Title: "t"}, nil, nil},
		{"empty label hides", links, func(c *Config) { c.ClassicalButton = "" }, nil},
		{"long label is cut", links, func(c *Config) { c.ClassicalButton = strings.Repeat("가", 40) },
			[]discord.Button{{Label: strings.Repeat("가", discord.MaxButtonLabelLen), URL: "https://classical/x"}}},
		{"overlong URL is dropped", Track{Title: "t", MusicURL: "https://music/" + strings.Repeat("x", discord.MaxButtonURLLen)}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			a := Build(tt.track, cfg, now)
			if a == nil || !slices.Equal(a.Buttons, tt.want) {
				t.Errorf("buttons = %+v, want %+v", a, tt.want)
			}
		})
	}
}

func TestPortraitBadge(t *testing.T) {
	withPortrait := func(edit func(*Track)) Track {
		tr := classical()
		tr.Portrait = "https://example.com/mozart.jpg"
		tr.PortraitName = "볼프강 아마데우스 모차르트"
		tr.PortraitURL = "https://example.com/composer"
		if edit != nil {
			edit(&tr)
		}
		return tr
	}
	tests := []struct {
		name                 string
		track                Track
		cfg                  func(*Config)
		image, hoverTxt, url string
	}{
		{"portrait over the cover", withPortrait(nil), nil, "https://example.com/mozart.jpg", "볼프강 아마데우스 모차르트", "https://example.com/composer"},
		{"paused badge wins", withPortrait(func(t *Track) { t.Playing = false }), nil, DefaultConfig().PausedImage, DefaultConfig().PausedText, ""},
		{"no portrait", classical(), nil, "", "", ""},
		{"disabled", withPortrait(nil), func(c *Config) { c.ShowPortrait = false }, "", "", ""},
		{"no name", withPortrait(func(t *Track) { t.PortraitName = "" }), nil, "", "", ""},
		{"no link", withPortrait(func(t *Track) { t.PortraitURL = "" }), nil, "https://example.com/mozart.jpg", "볼프강 아마데우스 모차르트", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			a := Build(tt.track, cfg, now)
			if a == nil || a.Assets == nil {
				t.Fatalf("got %+v, want assets", a)
			}
			if a.Assets.SmallImage != tt.image || a.Assets.SmallText != tt.hoverTxt || a.Assets.SmallURL != tt.url {
				t.Errorf("badge = %q / %q / %q, want %q / %q / %q",
					a.Assets.SmallImage, a.Assets.SmallText, a.Assets.SmallURL, tt.image, tt.hoverTxt, tt.url)
			}
		})
	}

	// Without a cover there is nothing to put the badge on.
	noCover := withPortrait(func(t *Track) { t.Artwork = "" })
	if a := Build(noCover, DefaultConfig(), now); a.Assets != nil {
		t.Errorf("assets without a cover = %+v, want nil", a.Assets)
	}
}
