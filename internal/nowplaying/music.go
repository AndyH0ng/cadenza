package nowplaying

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MusicBundleID is the bundle ID of the Music app.
const MusicBundleID = "com.apple.Music"

// musicScript prints the Music app's state and current track as fields
// separated by U+001F, or "stopped". It does not launch Music.
const musicScript = `if application "Music" is not running then return "stopped"
tell application "Music"
	set s to player state as text
	if s is not "playing" and s is not "paused" then return "stopped"
	try
		set t to current track
	on error
		return "stopped"
	end try
	set d to character id 31
	return s & d & name of t & d & artist of t & d & album of t & d & composer of t & d & (duration of t as text) & d & (player position as text) & d & persistent ID of t
end tell`

// ErrNotAuthorized is returned when macOS does not let this process control
// Music (System Settings → Privacy & Security → Automation).
var ErrNotAuthorized = errors.New("nowplaying: not allowed to control Music")

// MusicApp asks the Music app for its state with AppleScript. Now Playing
// reports only the app that played last, so this is how Music is followed
// while another app holds Now Playing.
type MusicApp struct {
	Osascript string // defaults to /usr/bin/osascript
}

// Query returns Music's state like a Now Playing snapshot; one without a
// title if Music is not running or not playing a track.
func (m MusicApp) Query(ctx context.Context) (Info, error) {
	bin := m.Osascript
	if bin == "" {
		bin = "/usr/bin/osascript"
	}
	out, err := exec.CommandContext(ctx, bin, "-e", musicScript).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "-1743") {
			return Info{}, ErrNotAuthorized
		}
		if errors.As(err, &exit) {
			return Info{}, fmt.Errorf("nowplaying: query Music: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return Info{}, fmt.Errorf("nowplaying: query Music: %w", err)
	}
	return parseMusic(strings.TrimSuffix(string(out), "\n"), time.Now())
}

// parseMusic parses musicScript's output, measured at now.
func parseMusic(out string, now time.Time) (Info, error) {
	if out == "stopped" {
		return Info{}, nil
	}
	f := strings.Split(out, "\x1f")
	if len(f) != 8 {
		return Info{}, fmt.Errorf("nowplaying: unexpected Music output %q", out)
	}
	info := Info{
		BundleID:  MusicBundleID,
		Title:     f[1],
		Artist:    f[2],
		Album:     f[3],
		Composer:  f[4],
		Playing:   f[0] == "playing",
		Duration:  parseSeconds(f[5]),
		Elapsed:   parseSeconds(f[6]),
		Timestamp: Timestamp{now},
	}
	if info.Playing {
		info.Rate = 1
	}
	// Now Playing reports the persistent ID as a signed decimal for library
	// tracks; catalog tracks it reports by catalog ID, which Music hides.
	if id, err := strconv.ParseUint(f[7], 16, 64); err == nil {
		info.TrackID = ID(strconv.FormatInt(int64(id), 10))
	}
	return info, nil
}

// parseSeconds parses an AppleScript real, whose decimal separator follows
// the system locale; malformed values give 0.
func parseSeconds(s string) float64 {
	v, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil {
		return 0
	}
	return v
}
