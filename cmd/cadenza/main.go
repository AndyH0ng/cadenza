// Command cadenza shows Apple Music's Now Playing as Discord Rich
// Presence, with localized work names and composers for classical music.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/AndyH0ng/cadenza/internal/app"
	"github.com/AndyH0ng/cadenza/internal/discord"
	"github.com/AndyH0ng/cadenza/internal/metadata"
	"github.com/AndyH0ng/cadenza/internal/nowplaying"
)

const adapterSubdir = "third_party/mediaremote-adapter"

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "cadenza:", err)
		os.Exit(1)
	}
}

func run() error {
	clientID := flag.String("client-id", os.Getenv("DISCORD_CLIENT_ID"), "Discord application ID (env DISCORD_CLIENT_ID)")
	adapterDir := flag.String("adapter", os.Getenv("CADENZA_ADAPTER"), "built mediaremote-adapter checkout (env CADENZA_ADAPTER; default "+adapterSubdir+" next to the binary)")
	country := flag.String("country", cmp.Or(os.Getenv("CADENZA_COUNTRY"), "kr"), "Apple Music storefront (env CADENZA_COUNTRY)")
	language := flag.String("language", cmp.Or(os.Getenv("CADENZA_LANGUAGE"), "ko-KR"), "metadata language (env CADENZA_LANGUAGE)")
	clearPaused := flag.Bool("clear-paused", false, "clear the status while paused")
	flag.Parse()

	if *clientID == "" {
		return errors.New("missing Discord application ID: set -client-id or DISCORD_CLIENT_ID")
	}
	adapter := nowplaying.Adapter{Dir: cmp.Or(*adapterDir, defaultAdapterDir())}
	if err := adapter.Check(); err != nil {
		return err
	}

	cfg := app.DefaultConfig()
	if *clearPaused {
		cfg.Paused = app.PausedClear
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dc := &discord.Client{ClientID: *clientID}
	defer dc.Close()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	a := &app.App{
		Config:   cfg,
		Source:   adapter,
		Catalog:  metadata.NewClient(*country, *language),
		Presence: dc,
		Music:    nowplaying.MusicApp{},
		Log:      log,
	}
	log.Info("started", "adapter", adapter.Dir, "client_id", *clientID)
	return a.Run(ctx)
}

// defaultAdapterDir returns the adapter next to the executable if there is
// one, else the one in the working directory.
func defaultAdapterDir() string {
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Join(filepath.Dir(exe), adapterSubdir)
			if _, err := os.Stat(dir); err == nil {
				return dir
			}
		}
	}
	return adapterSubdir
}
