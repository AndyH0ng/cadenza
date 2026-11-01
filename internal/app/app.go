// Package app wires Now Playing, catalog lookups and Discord together.
package app

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AndyH0ng/cadenza/internal/discord"
	"github.com/AndyH0ng/cadenza/internal/metadata"
	"github.com/AndyH0ng/cadenza/internal/nowplaying"
	"github.com/AndyH0ng/cadenza/internal/presence"
)

// PausedMode selects what is shown while playback is paused.
type PausedMode int

const (
	PausedShow  PausedMode = iota // keep the status, without a progress bar
	PausedClear                   // remove the status
)

// Config is the runtime configuration. See DefaultConfig for the defaults.
type Config struct {
	Presence presence.Config
	// Bundle IDs of players to show; empty means any player.
	Players []string
	Paused  PausedMode
	// Show the composer only for Classical tracks: other tracks list their
	// songwriters as composers.
	ClassicalOnlyComposer bool
	// LookupTimeout bounds all catalog lookups for one track.
	LookupTimeout time.Duration
	// LookupGrace is how long a new track waits for its lookups before it is
	// shown with Now Playing data alone. It is updated when they finish.
	LookupGrace time.Duration
	// Failed lookups are retried every LookupRetryInterval, up to
	// LookupRetries times, while the track is current. metadata.Client caches
	// failures for a minute, so a shorter interval gains nothing.
	LookupRetryInterval time.Duration
	LookupRetries       int
	// MinUpdateInterval spaces activity updates; Discord accepts about 5 per
	// 20 s. Changes in between are coalesced.
	MinUpdateInterval time.Duration
	// TimestampTolerance is the progress drift that is not worth an update.
	TimestampTolerance time.Duration
	// RetryInterval is the wait between attempts to reach Discord.
	RetryInterval time.Duration
	// RestartInterval is the wait before restarting the adapter.
	RestartInterval time.Duration
	// MusicPollInterval is how often Music is queried while another app
	// holds Now Playing.
	MusicPollInterval time.Duration
	// PausedCheck is how long Music is queried after Now Playing reports it
	// paused. Now Playing can miss a resume that quickly follows a pause,
	// e.g. while skipping tracks, and would show Music paused until the next
	// change.
	PausedCheck time.Duration
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
		Presence:              presence.DefaultConfig(),
		Players:               []string{nowplaying.MusicBundleID},
		Paused:                PausedShow,
		ClassicalOnlyComposer: true,
		LookupTimeout:         10 * time.Second,
		LookupGrace:           1500 * time.Millisecond,
		LookupRetryInterval:   time.Minute,
		LookupRetries:         10,
		MinUpdateInterval:     4 * time.Second,
		TimestampTolerance:    2 * time.Second,
		RetryInterval:         5 * time.Second,
		RestartInterval:       3 * time.Second,
		MusicPollInterval:     3 * time.Second,
		PausedCheck:           30 * time.Second,
	}
}

// Source streams Now Playing snapshots until it fails or ctx is done.
type Source interface {
	Stream(ctx context.Context, fn func(nowplaying.Info)) error
}

// Catalog looks up metadata missing from Now Playing.
type Catalog interface {
	Classical(ctx context.Context, trackID string) (*metadata.ClassicalTrack, error)
	CatalogID(ctx context.Context, q metadata.Query) (string, error)
	Artwork(ctx context.Context, catalogID string) (string, error)
	Song(ctx context.Context, catalogID string) (*metadata.Song, error)
	SongURL(catalogID string) string
}

// Music asks the Music app for its state, in place of Now Playing.
type Music interface {
	Query(ctx context.Context) (nowplaying.Info, error)
}

// Presence sets the Discord activity; nil clears it.
type Presence interface {
	SetActivity(ctx context.Context, a *discord.Activity) error
}

// App mirrors Now Playing to Discord.
type App struct {
	Config   Config
	Source   Source
	Catalog  Catalog
	Presence Presence
	// Music, if set, is followed directly while another app holds Now
	// Playing: macOS reports only the app that played last, so pausing Music
	// or opening a page with a video would otherwise hide a Music track.
	Music Music
	Log   *slog.Logger
	Now   func() time.Time // defaults to time.Now

	pollMu      sync.Mutex
	music       nowplaying.Info // last Music snapshot from Now Playing; zero once Music stopped
	stopPoll    context.CancelFunc
	pollDone    chan struct{}
	pollLimited bool // the poller checks a pause and stops after PausedCheck

	mu       sync.Mutex
	sender   *sender
	latest   nowplaying.Info   // last snapshot
	key      trackKey          // track of latest, or of the last one shown
	enriched *presence.Track   // lookup result for key; nil while pending
	last     *discord.Activity // last activity logged

	apiChanged atomic.Bool // a lookup failed with metadata.ErrAPIChanged
}

// trackKey identifies a track for its lookups; playback state is not part of it.
type trackKey struct {
	bundleID, title, artist, album, composer string
	id                                       nowplaying.ID
}

func keyOf(info nowplaying.Info) trackKey {
	return trackKey{info.BundleID, info.Title, info.Artist, info.Album, info.Composer, info.TrackID}
}

// Run blocks until ctx is done, restarting the source whenever it exits.
func (a *App) Run(ctx context.Context) error {
	if a.Now == nil {
		a.Now = time.Now
	}
	a.sender = newSender(a.Presence, a.Config, a.Log)
	go a.sender.run(ctx)

	for {
		err := a.Source.Stream(ctx, func(info nowplaying.Info) { a.snapshot(ctx, info) })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.Log.Warn("now playing source exited; restarting", "err", err, "in", a.Config.RestartInterval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.Config.RestartInterval):
		}
	}
}

// snapshot handles a Now Playing snapshot. While another app, or none, holds
// Now Playing, Music is polled instead until Now Playing reports it again or
// it is found stopped; Music may play behind the other app, also when this
// program starts. After a pause, Music is polled for PausedCheck to catch a
// resume that Now Playing missed.
func (a *App) snapshot(ctx context.Context, info nowplaying.Info) {
	if a.Music == nil || a.isPlayer(info) {
		a.stopPolling()
		a.pollMu.Lock()
		a.music = info
		a.pollMu.Unlock()
		a.update(ctx, info)
		if a.Music != nil && !info.Playing && a.Config.PausedCheck > 0 {
			a.startPolling(ctx, a.Config.PausedCheck)
		}
		return
	}
	a.pollMu.Lock()
	following := a.stopPoll != nil && !a.pollLimited && !closed(a.pollDone)
	a.pollMu.Unlock()
	if !following {
		a.stopPolling() // a pause check, which would end too soon
		a.startPolling(ctx, 0)
	}
}

// startPolling starts polling Music, for at most limit if it is not 0.
func (a *App) startPolling(ctx context.Context, limit time.Duration) {
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	var pctx context.Context
	var cancel context.CancelFunc
	if limit > 0 {
		pctx, cancel = context.WithTimeout(ctx, limit)
	} else {
		pctx, cancel = context.WithCancel(ctx)
	}
	a.stopPoll, a.pollDone, a.pollLimited = cancel, make(chan struct{}), limit > 0
	go a.poll(ctx, pctx, a.pollDone, limit == 0 && a.music.Title != "")
}

// closed reports whether c is closed; nil is not.
func closed(c <-chan struct{}) bool {
	if c == nil {
		return false
	}
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// isPlayer reports whether info is from a player that is shown.
func (a *App) isPlayer(info nowplaying.Info) bool {
	return info.Title != "" && (len(a.Config.Players) == 0 || slices.Contains(a.Config.Players, info.BundleID))
}

// poll shows Music's state every MusicPollInterval until pctx is done or
// Music stops. Lookups and sends use ctx, which outlives pctx. A denied
// Automation permission clears the status; other query errors keep it.
func (a *App) poll(ctx, pctx context.Context, done chan<- struct{}, wasShown bool) {
	defer close(done)
	if wasShown {
		a.Log.Info("another app took Now Playing; following Music directly")
	}
	tick := time.NewTicker(a.Config.MusicPollInterval)
	defer tick.Stop()
	failing := false // log only the first of consecutive failures
	for {
		info, err := a.Music.Query(pctx)
		if pctx.Err() != nil {
			return
		}
		if errors.Is(err, nowplaying.ErrNotAuthorized) {
			a.Log.Warn("cannot follow Music; allow it in System Settings → Privacy & Security → Automation", "err", err)
		} else if err != nil {
			if !failing {
				a.Log.Warn("cannot follow Music; retrying", "err", err, "every", a.Config.MusicPollInterval)
			}
			failing = true
			select {
			case <-pctx.Done():
				return
			case <-tick.C:
				continue
			}
		}
		failing = false
		a.pollMu.Lock()
		if last := a.music; info.Title != "" && info.Title == last.Title && info.Artist == last.Artist && info.Album == last.Album {
			// Keep the ID Now Playing reported (a catalog ID, which Music
			// hides), so that the lookup is reused.
			info.TrackID, info.Composer = last.TrackID, last.Composer
		}
		if info.Title == "" {
			a.music = nowplaying.Info{} // stopped or unknown: Now Playing reports it when it plays again
			if a.stopPoll != nil {
				a.stopPoll()     // releases pctx; ctx is used from here on
				a.stopPoll = nil // pollDone stays for stopPolling to wait on
			}
		}
		a.pollMu.Unlock()
		a.update(ctx, info)
		if info.Title == "" {
			return
		}
		select {
		case <-pctx.Done():
			return
		case <-tick.C:
		}
	}
}

// stopPolling stops polling Music and waits for the poller to finish, also
// if it is finishing on its own, so that it cannot show a stale state.
func (a *App) stopPolling() {
	a.pollMu.Lock()
	stop, done := a.stopPoll, a.pollDone
	a.stopPoll, a.pollDone = nil, nil
	a.pollMu.Unlock()
	if stop != nil {
		stop()
	}
	if done != nil {
		<-done
	}
}

// update shows info. A new track's lookups run in the background: the track
// is shown once they finish, or after LookupGrace with what Now Playing
// provides and again when they finish. Playback changes of the same track
// reuse the result and are shown at once.
func (a *App) update(ctx context.Context, info nowplaying.Info) {
	a.mu.Lock()
	a.latest = info
	var done chan struct{}
	if k := keyOf(info); a.shows(info) && k != a.key {
		a.key, a.enriched = k, nil
		done = make(chan struct{})
		go a.enrich(ctx, k, info, done)
	}
	a.mu.Unlock()

	if done != nil {
		t := time.NewTimer(a.Config.LookupGrace)
		select {
		case <-done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	a.publish()
}

// enrich looks up the track k and shows the result if k is still current.
// Failed lookups are retried while k stays current.
func (a *App) enrich(ctx context.Context, k trackKey, info nowplaying.Info, done chan<- struct{}) {
	t, ok := a.track(ctx, info)
	current := a.setEnriched(k, t)
	close(done)
	if !current {
		return
	}
	a.publish()
	for retry := 0; !ok && retry < a.Config.LookupRetries; retry++ {
		if !sleep(ctx, a.Config.LookupRetryInterval) || !a.isCurrent(k) {
			return
		}
		if t, ok = a.track(ctx, info); !a.setEnriched(k, t) {
			return
		}
		a.publish()
	}
}

// setEnriched stores t as the lookup result for k and reports whether k is
// still current; if not, t is dropped.
func (a *App) setEnriched(k trackKey, t presence.Track) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.key != k {
		return false
	}
	a.enriched = &t
	return true
}

func (a *App) isCurrent(k trackKey) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.key == k
}

// publish hands the activity for the latest snapshot to the sender.
func (a *App) publish() {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := a.latest
	var act *discord.Activity
	if a.shows(info) {
		t := a.baseTrack(info)
		if a.enriched != nil && keyOf(info) == a.key {
			t = *a.enriched
			setPlayback(&t, info) // the lookup started from an older snapshot
		}
		act = presence.Build(t, a.Config.Presence, a.Now())
	}
	if !presence.Equal(act, a.last, a.Config.TimestampTolerance) {
		a.logActivity(act, info.Playing)
		a.last = act
	}
	a.sender.want(act)
}

// shows reports whether info is shown at all.
func (a *App) shows(info nowplaying.Info) bool {
	if info.Title == "" {
		return false
	}
	if len(a.Config.Players) > 0 && !slices.Contains(a.Config.Players, info.BundleID) {
		return false
	}
	return info.Playing || a.Config.Paused != PausedClear
}

// baseTrack is the track as Now Playing shows it, before any lookup.
func (a *App) baseTrack(info nowplaying.Info) presence.Track {
	t := presence.Track{
		Title:  info.Title,
		Artist: info.Artist,
		Album:  metadata.AlbumName(info.Album),
	}
	if !a.Config.ClassicalOnlyComposer {
		t.Composer = info.Composer
	}
	setPlayback(&t, info)
	return t
}

func setPlayback(t *presence.Track, info nowplaying.Info) {
	t.Playing = info.Playing
	t.Duration = seconds(info.Duration)
	t.Elapsed = seconds(info.Elapsed)
	t.Rate = info.Rate
	t.At = info.Timestamp.Time
}

// track converts info and enriches it from the catalog. Lookup failures are
// logged and the track is shown with what Now Playing provides; ok reports
// whether all lookups succeeded.
func (a *App) track(ctx context.Context, info nowplaying.Info) (t presence.Track, ok bool) {
	t, ok = a.baseTrack(info), true
	if a.Config.LookupTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Config.LookupTimeout)
		defer cancel()
	}
	id, err := a.Catalog.CatalogID(ctx, metadata.Query{TrackID: string(info.TrackID), Title: info.Title, Album: info.Album})
	if err != nil {
		a.lookupFailed("catalog ID", err)
		ok = false
	}

	var c *metadata.ClassicalTrack
	if id != "" {
		t.MusicURL = a.Catalog.SongURL(id)
		if c, err = a.Catalog.Classical(ctx, id); err != nil {
			a.lookupFailed("classical", err)
			ok = false
		}
	}
	if c != nil {
		t.Work, t.Catalog, t.ComposerShort = c.Work, c.Catalog, c.ComposerShort
		if !metadata.RepeatsTitle(c.Movement, info.Title) {
			t.Movement = c.LocalMovement
		} else {
			// Apple repeats the track title when there is no movement; it may
			// still carry the piece number ("Liebestraum No. 3").
			t.Number = metadata.PieceNumber(info.Title, c.Work, c.Catalog)
		}
		t.Composer = cmp.Or(c.Composer, info.Composer)
		t.Artist = cmp.Or(c.Artist, t.Artist)
		t.Performers = c.Artist
		t.Artwork = c.Artwork
		t.Portrait, t.PortraitName, t.PortraitURL = c.ComposerImage, c.Composer, c.ComposerURL
		t.ClassicalURL = c.URL
	} else if id != "" {
		song, err := a.Catalog.Song(ctx, id)
		if err != nil {
			a.lookupFailed("song", err)
			ok = false
		}
		if song != nil {
			t.Artwork = song.Artwork
			if artist := song.Artist; artist != nil && a.Config.Presence.ShowPortrait {
				t.Portrait, t.PortraitName, t.PortraitURL = artist.Image, artist.Name, artist.URL
			}
		}
	}

	if t.Artwork == "" && id != "" { // iTunes needs no token
		art, err := a.Catalog.Artwork(ctx, id)
		if err != nil {
			a.lookupFailed("artwork", err)
			ok = false
		}
		t.Artwork = art
	}
	if ok && a.apiChanged.CompareAndSwap(true, false) {
		a.Log.Info("Apple API lookups work again")
	}
	return t, ok
}

// lookupFailed logs a failed lookup. A changed Apple API is logged once, as
// an error, until lookups succeed again: unlike a network error, it does not
// go away on its own.
func (a *App) lookupFailed(what string, err error) {
	if !errors.Is(err, metadata.ErrAPIChanged) {
		a.Log.Warn(what+" lookup failed", "err", err)
		return
	}
	if a.apiChanged.CompareAndSwap(false, true) {
		a.Log.Error("Apple API changed; tracks are shown without catalog data", "lookup", what, "err", err)
	}
}

func (a *App) logActivity(act *discord.Activity, playing bool) {
	switch {
	case act == nil:
		a.Log.Info("cleared")
	case !playing:
		a.Log.Info("paused", "details", act.Details, "state", act.State)
	default:
		a.Log.Info("playing", "details", act.Details, "state", act.State)
	}
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
