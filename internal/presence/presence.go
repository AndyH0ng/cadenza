// Package presence turns a track into a Discord activity. It is pure: no I/O,
// and the current time is passed in.
package presence

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AndyH0ng/cadenza/internal/discord"
)

// Track is everything known about the current track after enrichment.
type Track struct {
	Title, Artist, Album, Composer string
	// Set only for tracks in the Apple Music Classical catalog.
	Work, Catalog, Movement, ComposerShort string
	Number                                 string // piece within the work, if Movement is unknown
	Performers                             string
	Artwork                                string // cover URL
	// Photo shown as a badge over the cover: the composer's for Classical
	// tracks, else the artist's. Its name is the hover text, URL its link.
	Portrait, PortraitName, PortraitURL string
	// Links to the track; ClassicalURL is set only for Classical tracks.
	ClassicalURL, MusicURL string

	Playing  bool
	Duration time.Duration
	Elapsed  time.Duration // as of At
	Rate     float64       // 0 right after resuming
	At       time.Time     // when Elapsed was measured; zero if unknown
}

// placeholders maps the names usable as {name} in formats to their values.
func (t Track) placeholders() map[string]string {
	return map[string]string{
		"title":          t.Title,
		"artist":         t.Artist,
		"album":          t.Album,
		"composer":       t.Composer,
		"composer_short": t.ComposerShort,
		"work":           t.Work,
		"catalog":        t.Catalog,
		"movement":       t.Movement,
		"number":         t.Number,
		"performers":     t.Performers,
	}
}

// Config controls how a track is shown.
type Config struct {
	// Each field uses the first format whose placeholders are all non-empty.
	Details   []string
	State     []string
	LargeText []string // third line of a Listening card; also the cover hover text
	// Show Track.Portrait as a badge over the cover while playing.
	ShowPortrait bool
	// Which line replaces the app name in "Listening to …".
	StatusDisplayType int
	// Badge over the cover while paused, and its hover text. Without a cover
	// the image is shown in its place. The image is a URL or the name of an
	// art asset of the Discord application; "" shows no image.
	PausedImage, PausedText string
	// While paused, show the progress bar stuck at 0:00. Without it Discord
	// counts up the time since the activity started. Discord cannot pin the
	// bar at the paused position: it shows now − start, which only stops
	// changing when clamped to 0:00 or to the end.
	FreezePausedProgress bool
	// Label of the button linking to the track in Apple Music Classical, or
	// for other tracks in Apple Music; "" hides the button.
	ClassicalButton, MusicButton string
}

// DefaultConfig is the default layout:
//
//	Listening to <composer | artist>
//	<work>: <movement> | <title>
//	<composer | artist>
//	<performers | album>
func DefaultConfig() Config {
	return Config{
		Details:              []string{"{work}: {movement}", "{work} {number}번", "{work}", "{title}"},
		State:                []string{"{composer}", "{artist}"},
		LargeText:            []string{"{performers}", "{album}"},
		ShowPortrait:         true,
		StatusDisplayType:    discord.StatusDisplayState,
		PausedImage:          "paused",
		PausedText:           "일시정지됨",
		FreezePausedProgress: true,
		ClassicalButton:      "Apple Music에서 듣기",
		MusicButton:          "Apple Music에서 듣기",
	}
}

// Build returns the activity for t, or nil if no details line can be made.
// Playing tracks get the portrait as a badge over the cover;
// paused tracks get a paused badge and a progress bar frozen at 0:00.
func Build(t Track, cfg Config, now time.Time) *discord.Activity {
	fields := t.placeholders()
	a := &discord.Activity{
		Type:              discord.ActivityTypeListening,
		StatusDisplayType: cfg.StatusDisplayType,
		Details:           Render(cfg.Details, fields),
		State:             Render(cfg.State, fields),
	}
	if a.Details == "" {
		return nil
	}
	if t.Artwork != "" {
		a.Assets = &discord.Assets{LargeImage: t.Artwork, LargeText: Render(cfg.LargeText, fields)}
		// Discord shows a badge only over a cover. The paused badge replaces it.
		if cfg.ShowPortrait && t.Playing && t.Portrait != "" && t.PortraitName != "" {
			a.Assets.SmallImage, a.Assets.SmallText, a.Assets.SmallURL = t.Portrait, fit(t.PortraitName), t.PortraitURL
		}
	}
	if b, ok := button(t, cfg); ok {
		a.Buttons = []discord.Button{b}
	}
	if !t.Playing {
		switch {
		case cfg.PausedImage == "":
		case a.Assets == nil:
			a.Assets = &discord.Assets{LargeImage: cfg.PausedImage, LargeText: fit(cfg.PausedText)}
		default:
			a.Assets.SmallImage, a.Assets.SmallText = cfg.PausedImage, fit(cfg.PausedText)
		}
		if cfg.FreezePausedProgress && t.Duration > 0 {
			a.Timestamps = &discord.Timestamps{
				Start: frozenStart.UnixMilli(),
				End:   frozenStart.Add(t.Duration).UnixMilli(),
			}
		}
		return a
	}
	if t.Duration > 0 {
		a.Timestamps = progress(t, now)
	}
	return a
}

// button links to the track: in Classical if it is a Classical track, else
// in Apple Music.
func button(t Track, cfg Config) (discord.Button, bool) {
	label, url := cfg.MusicButton, t.MusicURL
	if t.ClassicalURL != "" {
		label, url = cfg.ClassicalButton, t.ClassicalURL
	}
	if label == "" || url == "" || len(url) > discord.MaxButtonURLLen {
		return discord.Button{}, false
	}
	if r := []rune(label); len(r) > discord.MaxButtonLabelLen {
		label = string(r[:discord.MaxButtonLabelLen])
	}
	return discord.Button{Label: label, URL: url}, true
}

// frozenStart is a fixed start time far in the future, so that a paused
// activity shows 0:00 and stays equal across updates.
var frozenStart = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

func progress(t Track, now time.Time) *discord.Timestamps {
	anchor, rate := t.At, t.Rate
	if rate <= 0 || anchor.IsZero() {
		// Right after resuming, the adapter still reports the paused position
		// with rate 0 and the time of the pause: playback continues from there now.
		anchor, rate = now, 1
	}
	scale := func(d time.Duration) time.Duration { return time.Duration(float64(d) / rate) }
	start := anchor.Add(-scale(t.Elapsed))
	end := anchor.Add(scale(max(t.Duration-t.Elapsed, 0)))
	return &discord.Timestamps{Start: start.UnixMilli(), End: end.UnixMilli()}
}

// Equal reports whether a and b show the same thing, ignoring progress
// timestamps that differ by at most tolerance (Now Playing has 1 s precision).
func Equal(a, b *discord.Activity, tolerance time.Duration) bool {
	if a == nil || b == nil {
		return a == b
	}
	ta, tb := a.Timestamps, b.Timestamps
	if (ta == nil) != (tb == nil) {
		return false
	}
	if ta != nil {
		tol := tolerance.Milliseconds()
		if absDiff(ta.Start, tb.Start) > tol || absDiff(ta.End, tb.End) > tol {
			return false
		}
	}
	ca, cb := *a, *b
	ca.Timestamps, cb.Timestamps = nil, nil
	return ca.Type == cb.Type && ca.StatusDisplayType == cb.StatusDisplayType &&
		ca.Details == cb.Details && ca.State == cb.State && equalAssets(ca.Assets, cb.Assets) &&
		slices.Equal(ca.Buttons, cb.Buttons)
}

func equalAssets(a, b *discord.Assets) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

var placeholderRE = regexp.MustCompile(`\{([a-z_]+)\}`)

// Render returns the first format whose placeholders all have non-empty
// values, filled in and fitted to Discord's length limits; "" if none does.
// Unknown placeholders count as empty.
func Render(formats []string, fields map[string]string) string {
	for _, f := range formats {
		matches := placeholderRE.FindAllStringSubmatch(f, -1)
		if len(matches) == 0 {
			continue
		}
		ok := true
		for _, m := range matches {
			if fields[m[1]] == "" {
				ok = false
				break
			}
		}
		if ok {
			return fit(placeholderRE.ReplaceAllStringFunc(f, func(m string) string {
				return fields[m[1:len(m)-1]]
			}))
		}
	}
	return ""
}

// fit truncates to MaxTextLen characters and pads to MinTextLen.
func fit(s string) string {
	if n := utf8.RuneCountInString(s); n > discord.MaxTextLen {
		s = string([]rune(s)[:discord.MaxTextLen])
	} else if n < discord.MinTextLen {
		s += strings.Repeat(" ", discord.MinTextLen-n)
	}
	return s
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
