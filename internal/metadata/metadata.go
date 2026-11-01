// Package metadata looks up catalog data that Now Playing lacks: localized
// classical work names (Apple Music Classical), covers and artists (Apple
// Music web player API, iTunes as a backup).
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultWebBase       = "https://classical.music.apple.com"
	DefaultClassicalBase = DefaultWebBase + "/api/classical/v10/query/tracksMetadata"
	DefaultAMPBase       = "https://amp-api.music.apple.com"
	DefaultITunesBase    = "https://itunes.apple.com"
	MusicBase            = "https://music.apple.com"

	artworkSize = 512
	userAgent   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)"
)

// ErrAPIChanged marks responses that the unofficial Apple APIs did not send
// before: an unexpected format, a missing developer token, or a refused
// request. Unlike network errors, these do not go away on their own.
var ErrAPIChanged = errors.New("metadata: Apple API changed")

// ClassicalTrack is localized metadata of a track in the Classical catalog.
type ClassicalTrack struct {
	Work    string // e.g. "피아노 협주곡 22번 내림마장조"
	Catalog string // catalog numbers, e.g. "K. 482, KV 482"
	// Movement is e.g. "II. Andante", as Apple sends it: Apple does not
	// localize movements. For works without movements Apple repeats the full
	// track title here.
	Movement string
	// LocalMovement is Movement for display, localized where possible
	// ("No. 1 in A-Flat Major" → "1번 내림가장조"); "" if it only repeats Work.
	LocalMovement string
	Composer      string
	ComposerShort string // e.g. "W. A. 모차르트"
	Artist        string
	Artwork       string // cover URL
	ComposerImage string // portrait URL; "" if Apple has none
	ComposerURL   string // the composer in the Classical web player; "" if unknown
	URL           string // the track in the Classical web player; "" if unknown
}

// Song is a track in the Apple Music catalog.
type Song struct {
	Artwork string  // cover URL; "" if Apple has none
	Artist  *Artist // main artist; nil if the catalog has none
}

// Artist is the main artist of a track in the Apple Music catalog.
type Artist struct {
	Name  string
	Image string // photo URL; "" if Apple has none
	URL   string // the artist's Apple Music page
}

// Query identifies a track whose catalog ID may be unknown.
type Query struct {
	TrackID      string
	Title, Album string
}

// Client caches successful lookups, including "not found", for good. Failures
// are cached for FailureBackoff, so that an unreachable API is not waited for
// on every Now Playing update. It is safe for concurrent use.
type Client struct {
	HTTP          *http.Client
	WebBase       string // web player, source of the developer token
	ClassicalBase string
	AMPBase       string
	ITunesBase    string
	Storefront    string // e.g. "kr"
	Language      string // e.g. "ko-KR"; without it Classical answers in English
	// How long a failed lookup returns its error before it is retried.
	FailureBackoff time.Duration

	now func() time.Time // defaults to time.Now; set by tests

	mu         sync.Mutex
	classical  map[string]result[*ClassicalTrack]
	artwork    map[string]result[string]
	songs      map[string]result[*Song]
	catalogIDs map[Query]result[string]
	token      string
	tokenExp   time.Time
}

// result is a cached lookup.
type result[V any] struct {
	v       V
	err     error
	retryAt time.Time // for failures
}

// NewClient returns a client for the public Apple endpoints.
func NewClient(storefront, language string) *Client {
	return &Client{
		HTTP:          &http.Client{Timeout: 8 * time.Second},
		WebBase:       DefaultWebBase,
		ClassicalBase: DefaultClassicalBase,
		AMPBase:       DefaultAMPBase,
		ITunesBase:    DefaultITunesBase,
		Storefront:    storefront,
		Language:      language,

		FailureBackoff: time.Minute,
	}
}

// Classical returns the track's localized metadata, or nil if the track is
// not in the Classical catalog.
//
// It uses the unofficial, unauthenticated API of the Classical web player.
// Note: adding platform=web, as the web player does, fails with HTTP 400.
func (c *Client) Classical(ctx context.Context, trackID string) (*ClassicalTrack, error) {
	return lookup(ctx, c, &c.classical, trackID, func() (*ClassicalTrack, error) {
		return c.fetchClassical(ctx, trackID)
	})
}

func (c *Client) fetchClassical(ctx context.Context, trackID string) (*ClassicalTrack, error) {
	q := url.Values{"kind": {"song"}, "ids": {trackID}, "l": {c.Language}}
	var resp struct {
		TrackMetadataMap map[string]struct {
			WorkTitle            string `json:"workTitle"`
			WorkSubtitle         string `json:"workSubtitle"`
			MovementOrTrackTitle string `json:"movementOrTrackTitle"`
			ComposerTitle        string `json:"composerTitle"`
			ComposerShortTitle   string `json:"composerShortTitle"`
			DisplayArtists       string `json:"displayArtists"`
			AlbumArt             struct {
				URL string `json:"url"`
			} `json:"albumArt"`
			ComposerImage struct {
				URL string `json:"url"`
			} `json:"composerImage"`
			ComposerAction struct {
				URL string `json:"url"` // "/query/view/kr/artist/12374"
			} `json:"composerAction"`
			AlbumAction struct {
				URL string `json:"url"` // "/query/view/kr/album/1866870581"
			} `json:"albumAction"`
		} `json:"trackMetadataMap"`
	}
	u := fmt.Sprintf("%s/%s?%s", c.ClassicalBase, url.PathEscape(c.Storefront), q.Encode())
	if err := c.getJSON(ctx, u, nil, &resp); err != nil {
		return nil, fmt.Errorf("metadata: classical %s: %w", trackID, err)
	}
	var track *ClassicalTrack
	if m, ok := resp.TrackMetadataMap[trackID]; ok {
		work := workName(m.WorkTitle)
		track = &ClassicalTrack{
			Work:          work,
			Catalog:       m.WorkSubtitle,
			Movement:      m.MovementOrTrackTitle,
			LocalMovement: localMovement(c.Language, work, m.MovementOrTrackTitle),
			Composer:      m.ComposerTitle,
			ComposerShort: m.ComposerShortTitle,
			Artist:        m.DisplayArtists,
			Artwork:       artworkFromTemplate(m.AlbumArt.URL),
			ComposerImage: artworkFromTemplate(m.ComposerImage.URL),
			ComposerURL:   c.webURL(m.ComposerAction.URL, "artist"),
			URL:           c.trackURL(m.AlbumAction.URL, trackID),
		}
	}
	return track, nil
}

// webURL turns an API action URL such as "/query/view/kr/artist/12374" into
// the web player's page, https://classical.music.apple.com/kr/artist/12374,
// or "" if the action does not lead to a kind page.
func (c *Client) webURL(action, kind string) string {
	id := action[strings.LastIndexByte(action, '/')+1:]
	if !strings.HasSuffix(action, "/"+kind+"/"+id) || !isCatalogID(id) {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s/%s", c.WebBase, url.PathEscape(c.Storefront), kind, id)
}

// trackURL returns the link the Classical web player shares for a track,
// e.g. https://classical.music.apple.com/kr/album/1866870581?i=1866870772.
func (c *Client) trackURL(albumAction, trackID string) string {
	album := c.webURL(albumAction, "album")
	if album == "" {
		return ""
	}
	return album + "?" + url.Values{"i": {trackID}}.Encode()
}

// SongURL returns the track's Apple Music link, which redirects to its
// canonical page.
func (c *Client) SongURL(catalogID string) string {
	return fmt.Sprintf("%s/%s/song/%s", MusicBase, url.PathEscape(c.Storefront), url.PathEscape(catalogID))
}

// CatalogID returns the catalog ID of the track, or "" if it cannot be found.
//
// Now Playing reports a catalog ID for a track streamed from the catalog and
// a library ID (a random 64-bit number) for a track in the library. A
// library track is found by searching Storefront in Language for exactly its
// title and album, which are localized: the US storefront names "로켓트"
// "Love". iTunes Search is no alternative; it finds nothing in the Korean
// storefront. The search uses the web player API; see developerToken.
func (c *Client) CatalogID(ctx context.Context, q Query) (string, error) {
	if isCatalogID(q.TrackID) {
		return q.TrackID, nil
	}
	if q.Title == "" || q.Album == "" {
		return "", nil
	}
	q.TrackID = "" // library IDs are local; title and album identify the track
	return lookup(ctx, c, &c.catalogIDs, q, func() (string, error) {
		return c.searchCatalogID(ctx, q)
	})
}

func (c *Client) searchCatalogID(ctx context.Context, q Query) (string, error) {
	u := fmt.Sprintf("%s/v1/catalog/%s/search?%s", c.AMPBase, url.PathEscape(c.Storefront), url.Values{
		"term": {q.Title + " " + q.Album}, "types": {"songs"}, "limit": {"25"}, "l": {c.Language},
	}.Encode())
	var resp struct {
		Results struct {
			Songs struct {
				Data []struct {
					ID         string `json:"id"`
					Attributes struct {
						Name      string `json:"name"`
						AlbumName string `json:"albumName"`
					} `json:"attributes"`
				} `json:"data"`
			} `json:"songs"`
		} `json:"results"`
	}
	if err := c.getAMP(ctx, u, &resp); err != nil {
		return "", fmt.Errorf("metadata: catalog search: %w", err)
	}
	for _, s := range resp.Results.Songs.Data {
		if s.Attributes.Name == q.Title && s.Attributes.AlbumName == q.Album {
			return s.ID, nil
		}
	}
	return "", nil
}

// Song returns the track's cover and main artist, or nil if the catalog does
// not have the track. It uses the unofficial web player API; see
// developerToken.
func (c *Client) Song(ctx context.Context, catalogID string) (*Song, error) {
	return lookup(ctx, c, &c.songs, catalogID, func() (*Song, error) {
		return c.fetchSong(ctx, catalogID)
	})
}

func (c *Client) fetchSong(ctx context.Context, catalogID string) (*Song, error) {
	u := fmt.Sprintf("%s/v1/catalog/%s/songs/%s?%s", c.AMPBase, url.PathEscape(c.Storefront), url.PathEscape(catalogID), url.Values{
		"include": {"artists"}, "l": {c.Language},
	}.Encode())
	type artwork struct {
		URL string `json:"url"`
	}
	var resp struct {
		Data []struct {
			Attributes struct {
				Artwork artwork `json:"artwork"`
			} `json:"attributes"`
			Relationships struct {
				Artists struct {
					Data []struct {
						Attributes struct {
							Name    string  `json:"name"`
							URL     string  `json:"url"`
							Artwork artwork `json:"artwork"`
						} `json:"attributes"`
					} `json:"data"`
				} `json:"artists"`
			} `json:"relationships"`
		} `json:"data"`
	}
	if err := c.getAMP(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("metadata: song %s: %w", catalogID, err)
	}
	if len(resp.Data) == 0 {
		return nil, nil
	}
	d := resp.Data[0]
	song := &Song{Artwork: artworkFromTemplate(d.Attributes.Artwork.URL)}
	if artists := d.Relationships.Artists.Data; len(artists) > 0 {
		a := artists[0].Attributes
		song.Artist = &Artist{Name: a.Name, Image: artworkFromTemplate(a.Artwork.URL), URL: a.URL}
	}
	return song, nil
}

// Artwork returns a cover URL from the iTunes catalog, or "" if none. It
// needs no token, so it backs up Song. (Discord shows images only by URL, so
// the artwork Now Playing provides cannot be used.)
func (c *Client) Artwork(ctx context.Context, catalogID string) (string, error) {
	return lookup(ctx, c, &c.artwork, catalogID, func() (string, error) {
		return c.fetchArtwork(ctx, catalogID)
	})
}

func (c *Client) fetchArtwork(ctx context.Context, catalogID string) (string, error) {
	u := fmt.Sprintf("%s/lookup?%s", c.ITunesBase, url.Values{"id": {catalogID}, "country": {c.Storefront}}.Encode())
	var resp struct {
		Results []struct {
			ArtworkURL100 string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, u, nil, &resp); err != nil {
		return "", fmt.Errorf("metadata: itunes artwork: %w", err)
	}
	if len(resp.Results) == 0 {
		return "", nil
	}
	return strings.Replace(resp.Results[0].ArtworkURL100, "100x100bb", fmt.Sprintf("%dx%dbb", artworkSize, artworkSize), 1), nil
}

// maxCatalogID bounds catalog IDs. Those seen have at most 10 digits (the
// largest of 5,192 crawled tracks: 6821001624); library IDs have about 19.
const maxCatalogID = 1e12

// isCatalogID reports whether id looks like a catalog ID rather than a
// library ID.
func isCatalogID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && n < maxCatalogID
}

// statusError is a non-200 HTTP response.
type statusError struct{ code int }

func (e statusError) Error() string {
	return fmt.Sprintf("HTTP %d %s", e.code, http.StatusText(e.code))
}

// Is reports a request Apple refuses outright as ErrAPIChanged. 404 is left
// out: the API uses it for tracks not in the catalog.
func (e statusError) Is(target error) bool {
	return target == ErrAPIChanged && (e.code == http.StatusBadRequest || e.code == http.StatusForbidden || e.code == http.StatusGone)
}

func (c *Client) get(ctx context.Context, u string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header = header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

// getAMP gets JSON from the web player API with the developer token, renewing
// the token once if Apple rejects it.
func (c *Client) getAMP(ctx context.Context, u string, v any) error {
	for retried := false; ; retried = true {
		token, err := c.developerToken(ctx)
		if err != nil {
			return err
		}
		err = c.getJSON(ctx, u, http.Header{"Authorization": {"Bearer " + token}, "Origin": {c.WebBase}}, v)
		if se := (statusError{}); errors.As(err, &se) && se.code == http.StatusUnauthorized && !retried {
			c.dropToken() // rotated by Apple; fetch the current one
			continue
		}
		return err
	}
}

func (c *Client) getJSON(ctx context.Context, u string, header http.Header, v any) error {
	b, err := c.get(ctx, u, header)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %v", ErrAPIChanged, err)
	}
	return nil
}

// lookup returns the cached result for k, or calls fetch and caches its
// result: successes for good, failures until FailureBackoff has passed.
// Failures because ctx ended are not cached: they say nothing about the API.
func lookup[K comparable, V any](ctx context.Context, c *Client, m *map[K]result[V], k K, fetch func() (V, error)) (V, error) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	c.mu.Lock()
	r, ok := (*m)[k]
	c.mu.Unlock()
	if ok && (r.err == nil || now().Before(r.retryAt)) {
		return r.v, r.err
	}

	r.v, r.err = fetch()
	if r.err != nil && ctx.Err() != nil {
		return r.v, r.err
	}
	r.retryAt = now().Add(c.FailureBackoff)
	c.mu.Lock()
	if *m == nil {
		*m = make(map[K]result[V])
	}
	(*m)[k] = r
	c.mu.Unlock()
	return r.v, r.err
}

// releaseTypeRE matches the release type Apple appends to album names.
var releaseTypeRE = regexp.MustCompile(`\s+[-–—]\s+(?:Single|EP)$`)

// AlbumName drops the release type Apple appends to album names:
// "로켓트 - Single" becomes "로켓트", "Mozart: … K. 622 - EP" becomes
// "Mozart: … K. 622". Searches must use the name as Apple has it.
func AlbumName(s string) string {
	return releaseTypeRE.ReplaceAllString(s, "")
}

// workName drops the middle dot Apple puts before a work's nickname:
// "발라드 1번 사단조 · ‘폴란드 발라드’" becomes "발라드 1번 사단조 ‘폴란드 발라드’".
func workName(s string) string {
	return strings.ReplaceAll(s, " · ‘", " ‘")
}

// artworkFromTemplate fills Apple's "{w}x{h}{c}.{f}" image URL template.
func artworkFromTemplate(tmpl string) string {
	if tmpl == "" {
		return ""
	}
	return strings.NewReplacer(
		"{w}", fmt.Sprint(artworkSize), "{h}", fmt.Sprint(artworkSize), "{c}", "bb", "{f}", "jpg",
	).Replace(tmpl)
}
