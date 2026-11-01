package metadata

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Trimmed from a real tracksMetadata response (l=ko-KR).
const classicalBody = `{"trackMetadataMap":{"1866870772":{
	"displayArtists":"밤베르크 교향악단, 만프레드 호네크, 얀 리시에츠키",
	"albumTitle":"Mozart: Piano Concertos Nos. 9 & 22",
	"albumArt":{"url":"https://is1-ssl.mzstatic.com/image/thumb/x/25UM1IM55465.rgb.jpg/{w}x{h}{c}.{f}"},
	"albumAction":{"url":"/query/view/kr/album/1866870581"},
	"composerImage":{"url":"https://is1-ssl.mzstatic.com/image/thumb/y/mza_1.png/{w}x{h}{c}.{f}"},
	"composerAction":{"url":"/query/view/kr/artist/12374"},
	"composerTitle":"볼프강 아마데우스 모차르트","composerShortTitle":"W. A. 모차르트",
	"workTitle":"피아노 협주곡 22번 내림마장조 · ‘별칭’","workSubtitle":"K. 482, KV 482",
	"movementOrTrackTitle":"II. Andante"}},"page":0,"pageSize":1}`

func TestAlbumName(t *testing.T) {
	tests := map[string]string{
		"로켓트 - Single": "로켓트",
		"Mozart: Concerto for Clarinet and Orchestra in A Major, K. 622 - EP": "Mozart: Concerto for Clarinet and Orchestra in A Major, K. 622",
		"Dance Monkey – Single":                            "Dance Monkey", // en dash
		"Chopin: Piano Concerto No. 1 & Ballades (Deluxe)": "Chopin: Piano Concerto No. 1 & Ballades (Deluxe)",
		"Single Ladies":                                    "Single Ladies",
		"EP - Single":                                      "EP",
		"The Singles":                                      "The Singles",
	}
	for in, want := range tests {
		if got := AlbumName(in); got != want {
			t.Errorf("AlbumName(%q) = %q, want %q", in, got, want)
		}
	}
}

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("kr", "ko-KR")
	c.HTTP = srv.Client()
	c.WebBase = srv.URL + "/web"
	c.ClassicalBase = srv.URL + "/classical"
	c.AMPBase = srv.URL + "/amp"
	c.ITunesBase = srv.URL
	return c, &hits
}

func TestClassical(t *testing.T) {
	c, hits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/classical/kr" || q.Get("kind") != "song" || q.Get("l") != "ko-KR" || q.Has("platform") {
			t.Errorf("unexpected request %s", r.URL)
		}
		if q.Get("ids") == "1866870772" {
			w.Write([]byte(classicalBody))
			return
		}
		w.Write([]byte(`{"trackMetadataMap":{}}`)) // not a Classical track
	})
	ctx := context.Background()

	got, err := c.Classical(ctx, "1866870772")
	if err != nil {
		t.Fatal(err)
	}
	want := ClassicalTrack{
		Work:          "피아노 협주곡 22번 내림마장조 ‘별칭’", // middle dot dropped
		Catalog:       "K. 482, KV 482",
		Movement:      "II. Andante",
		LocalMovement: "II. Andante",
		Composer:      "볼프강 아마데우스 모차르트",
		ComposerShort: "W. A. 모차르트",
		Artist:        "밤베르크 교향악단, 만프레드 호네크, 얀 리시에츠키",
		Artwork:       "https://is1-ssl.mzstatic.com/image/thumb/x/25UM1IM55465.rgb.jpg/512x512bb.jpg",
		ComposerImage: "https://is1-ssl.mzstatic.com/image/thumb/y/mza_1.png/512x512bb.jpg",
		ComposerURL:   c.WebBase + "/kr/artist/12374",
		URL:           c.WebBase + "/kr/album/1866870581?i=1866870772",
	}
	if got == nil || *got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}

	if pop, err := c.Classical(ctx, "1657231962"); err != nil || pop != nil {
		t.Errorf("non-classical = %+v, %v; want nil, nil", pop, err)
	}

	// Both results, including "not found", are cached.
	c.Classical(ctx, "1866870772")
	c.Classical(ctx, "1657231962")
	if n := hits.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestClassicalFailureBackoff(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	c, hits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusBadRequest)
			return
		}
		w.Write([]byte(classicalBody))
	})
	now := time.Now()
	c.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.Classical(ctx, "1866870772"); err == nil {
		t.Fatal("want error on HTTP 400")
	}
	fail.Store(false)

	// Within the backoff the failure is returned without a request.
	if _, err := c.Classical(ctx, "1866870772"); err == nil {
		t.Error("want the cached error within the backoff")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests within the backoff, want 1", n)
	}

	now = now.Add(c.FailureBackoff)
	if got, err := c.Classical(ctx, "1866870772"); err != nil || got == nil {
		t.Errorf("after the backoff = %+v, %v; want a track", got, err)
	}
}

func TestClassicalURL(t *testing.T) {
	c := NewClient("kr", "ko-KR")
	tests := map[string]string{
		"/query/view/kr/album/1866870581": "https://classical.music.apple.com/kr/album/1866870581?i=42",
		"/query/view/kr/artist/12374":     "", // not an album
		"/query/view/kr/album/":           "",
		"":                                "",
	}
	for action, want := range tests {
		if got := c.trackURL(action, "42"); got != want {
			t.Errorf("trackURL(%q) = %q, want %q", action, got, want)
		}
	}
	if got, want := c.webURL("/query/view/kr/artist/12374", "artist"), "https://classical.music.apple.com/kr/artist/12374"; got != want {
		t.Errorf("composer webURL = %q, want %q", got, want)
	}
	if got := c.webURL("/query/view/kr/work/wolfgang-amadeus-mozart-1756-pp248", "artist"); got != "" {
		t.Errorf("work as artist = %q, want \"\"", got)
	}
	if got, want := c.SongURL("1866870772"), "https://music.apple.com/kr/song/1866870772"; got != want {
		t.Errorf("SongURL = %q, want %q", got, want)
	}
}

func TestCallerDeadlineNotCached(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			<-r.Context().Done()
			return
		}
		w.Write([]byte(classicalBody))
	})
	// The caller ran out of time, e.g. after a slow earlier lookup: that says
	// nothing about this API, so the next call must not get a cached failure.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Classical(ctx, "1866870772"); err == nil {
		t.Fatal("want error after the caller's deadline")
	}
	slow.Store(false)
	if got, err := c.Classical(context.Background(), "1866870772"); err != nil || got == nil {
		t.Errorf("next call = %+v, %v; want a track", got, err)
	}
}

func TestArtwork(t *testing.T) {
	c, hits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); r.URL.Path == "/lookup" && q.Get("id") == "1657231962" && q.Get("country") == "kr" {
			w.Write([]byte(`{"results":[{"artworkUrl100":"https://img/f6/100x100bb.jpg"}]}`))
			return
		}
		w.Write([]byte(`{"results":[]}`))
	})
	ctx := context.Background()
	for id, want := range map[string]string{"1657231962": "https://img/f6/512x512bb.jpg", "1": ""} {
		if got, err := c.Artwork(ctx, id); err != nil || got != want {
			t.Errorf("Artwork(%s) = %q, %v; want %q", id, got, err, want)
		}
	}
	c.Artwork(ctx, "1657231962")
	if n := hits.Load(); n != 2 {
		t.Errorf("%d requests, want 2 (cached)", n)
	}
}

// fakeJWT returns a token whose payload expires at exp.
func fakeJWT(exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "eyJhbGciOiJFUzI1NiJ9." + payload + ".c2ln"
}

// webPlayer serves a fake Classical web page and bundle embedding token(),
// and counts how often the bundle is fetched.
func webPlayer(t *testing.T, token func() string, w http.ResponseWriter, r *http.Request, bundles *atomic.Int32) bool {
	t.Helper()
	switch r.URL.Path {
	case "/web/kr":
		w.Write([]byte(`<script type="module" crossorigin src="/assets/index~01b654f647.js"></script>`))
	case "/web/assets/index~01b654f647.js":
		bundles.Add(1)
		w.Write([]byte(`var x="` + token() + `";`))
	default:
		return false
	}
	return true
}

// Trimmed from a real Korean storefront search.
const searchBody = `{"results":{"songs":{"data":[
	{"id":"1522532995","attributes":{"name":"그대 이제 안녕","albumName":"Chapter_01"}},
	{"id":"1661919663","attributes":{"name":"로켓트","albumName":"로켓트 - Single"}},
	{"id":"1452618211","attributes":{"name":"Piano Concerto No. 1 in E Minor, Op. 11: I. Allegro maestoso","albumName":"Chopin: Piano Concerto No. 1 & Ballades (Deluxe)"}}]}}}`

func TestCatalogID(t *testing.T) {
	token := fakeJWT(time.Now().Add(24 * time.Hour))
	var bundles, searches atomic.Int32
	var c *Client // the handler reads c.WebBase, set once the server is up
	c, _ = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if webPlayer(t, func() string { return token }, w, r, &bundles) {
			return
		}
		q := r.URL.Query()
		if r.URL.Path != "/amp/v1/catalog/kr/search" || q.Get("l") != "ko-KR" || q.Get("types") != "songs" ||
			r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Origin") != c.WebBase {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		searches.Add(1)
		w.Write([]byte(searchBody))
	})
	ctx := context.Background()
	tests := []struct {
		name string
		q    Query
		want string
	}{
		{"catalog ID is kept without a request", Query{TrackID: "1866870772", Title: "x", Album: "y"}, "1866870772"},
		{"library ID, Korean pop", Query{TrackID: "4385621753610067973", Title: "로켓트", Album: "로켓트 - Single"}, "1661919663"},
		{"library ID, classical", Query{TrackID: "-3976930821992376965",
			Title: "Piano Concerto No. 1 in E Minor, Op. 11: I. Allegro maestoso", Album: "Chopin: Piano Concerto No. 1 & Ballades (Deluxe)"}, "1452618211"},
		{"same title, other album", Query{TrackID: "-1", Title: "로켓트", Album: "Other"}, ""},
		{"nothing to search", Query{TrackID: "-1"}, ""},
		{"cached", Query{TrackID: "-2", Title: "로켓트", Album: "로켓트 - Single"}, "1661919663"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := c.CatalogID(ctx, tt.q); err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if n := searches.Load(); n != 3 {
		t.Errorf("%d searches, want 3", n)
	}
	if n := bundles.Load(); n != 1 {
		t.Errorf("token fetched %d times, want once", n)
	}
}

func TestCatalogIDTokenRenewal(t *testing.T) {
	var current atomic.Value
	current.Store(fakeJWT(time.Now().Add(24 * time.Hour)))
	var bundles atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if webPlayer(t, func() string { return current.Load().(string) }, w, r, &bundles) {
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+current.Load().(string) {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(searchBody))
	})
	ctx := context.Background()
	if _, err := c.CatalogID(ctx, Query{TrackID: "-1", Title: "로켓트", Album: "a"}); err != nil {
		t.Fatal(err)
	}

	// Apple rotates the token: one 401, then the new token is used.
	current.Store(fakeJWT(time.Now().Add(48 * time.Hour)))
	if got, err := c.CatalogID(ctx, Query{TrackID: "-1", Title: "로켓트", Album: "로켓트 - Single"}); err != nil || got != "1661919663" {
		t.Errorf("after rotation: %q, %v", got, err)
	}
	if n := bundles.Load(); n != 2 {
		t.Errorf("token fetched %d times, want 2", n)
	}

	// A token about to expire is renewed before use.
	current.Store(fakeJWT(time.Now().Add(time.Minute)))
	c.dropToken()
	c.CatalogID(ctx, Query{TrackID: "-1", Title: "x", Album: "1"}) // caches the short-lived token
	c.CatalogID(ctx, Query{TrackID: "-1", Title: "x", Album: "2"})
	if n := bundles.Load(); n != 4 {
		t.Errorf("token fetched %d times, want 4 (expiring token renewed)", n)
	}
}

func TestCatalogIDNoToken(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>no bundle</html>`))
	})
	if _, err := c.CatalogID(context.Background(), Query{TrackID: "-1", Title: "로켓트", Album: "a"}); !errors.Is(err, ErrAPIChanged) {
		t.Errorf("err = %v, want ErrAPIChanged when the web player has no token", err)
	}
}

func TestAPIChanged(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		changed bool
	}{
		{"unexpected format", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`<html>`)) }, true},
		{"refused", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "", http.StatusBadRequest) }, true},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "", http.StatusBadGateway) }, false},
		{"not found", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "", http.StatusNotFound) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, tt.handler)
			_, err := c.Classical(context.Background(), "1")
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrAPIChanged); got != tt.changed {
				t.Errorf("errors.Is(%v, ErrAPIChanged) = %v, want %v", err, got, tt.changed)
			}
		})
	}
}

// Trimmed from a real songs/1657231962?include=artists response.
const songArtistsBody = `{"data":[{"id":"1657231962",
	"attributes":{"artwork":{"url":"https://is1-ssl.mzstatic.com/image/thumb/Music112/v4/x/196922287107_Cover.jpg/{w}x{h}bb.jpg"}},
	"relationships":{"artists":{"data":[{"id":"1635469693","attributes":{
	"name":"뉴진스","url":"https://music.apple.com/kr/artist/%EB%89%B4%EC%A7%84%EC%8A%A4/1635469693",
	"artwork":{"url":"https://is1-ssl.mzstatic.com/image/thumb/AMCArtistImages211/v4/x/ami.png/{w}x{h}bb.jpg"}}}]}}}]}`

func TestSong(t *testing.T) {
	token := fakeJWT(time.Now().Add(24 * time.Hour))
	var bundles, lookups atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if webPlayer(t, func() string { return token }, w, r, &bundles) {
			return
		}
		q := r.URL.Query()
		if q.Get("include") != "artists" || q.Get("l") != "ko-KR" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		lookups.Add(1)
		switch r.URL.Path {
		case "/amp/v1/catalog/kr/songs/1657231962":
			w.Write([]byte(songArtistsBody))
		default:
			w.Write([]byte(`{"data":[{"id":"9","relationships":{"artists":{"data":[]}}}]}`))
		}
	})
	ctx := context.Background()

	got, err := c.Song(ctx, "1657231962")
	if err != nil {
		t.Fatal(err)
	}
	wantArt := "https://is1-ssl.mzstatic.com/image/thumb/Music112/v4/x/196922287107_Cover.jpg/512x512bb.jpg"
	want := Artist{
		Name:  "뉴진스",
		Image: "https://is1-ssl.mzstatic.com/image/thumb/AMCArtistImages211/v4/x/ami.png/512x512bb.jpg",
		URL:   "https://music.apple.com/kr/artist/%EB%89%B4%EC%A7%84%EC%8A%A4/1635469693",
	}
	if got == nil || got.Artwork != wantArt || got.Artist == nil || *got.Artist != want {
		t.Errorf("got  %+v\nwant artwork %s, artist %+v", got, wantArt, want)
	}
	if s, err := c.Song(ctx, "9"); err != nil || s == nil || s.Artist != nil || s.Artwork != "" {
		t.Errorf("song without artist = %+v, %v; want empty song", s, err)
	}
	c.Song(ctx, "1657231962")
	if n := lookups.Load(); n != 2 {
		t.Errorf("%d lookups, want 2 (cached)", n)
	}
}
