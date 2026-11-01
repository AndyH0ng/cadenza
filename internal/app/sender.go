package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/AndyH0ng/cadenza/internal/discord"
	"github.com/AndyH0ng/cadenza/internal/presence"
)

// sender delivers the latest wanted activity to Discord, at most once per
// MinUpdateInterval. Intermediate states are dropped. When the connection is
// lost it resends, retrying every RetryInterval, so a restarted Discord client
// gets the status back. An activity Discord rejects is not retried: it would
// fail the same way.
type sender struct {
	presence Presence
	lost     <-chan struct{} // nil if presence cannot tell
	cfg      Config
	log      *slog.Logger

	mu     sync.Mutex
	wanted *discord.Activity
	sent   *discord.Activity // nil: nothing shown, which is also Discord's initial state
	notify chan struct{}
}

// LostNotifier is implemented by a Presence that can tell when Discord
// dropped the connection, and with it the activity.
type LostNotifier interface {
	Lost() <-chan struct{}
}

func newSender(p Presence, cfg Config, log *slog.Logger) *sender {
	s := &sender{presence: p, cfg: cfg, log: log, notify: make(chan struct{}, 1)}
	if l, ok := p.(LostNotifier); ok {
		s.lost = l.Lost()
	}
	return s
}

// want records a as the activity to show. It never blocks.
func (s *sender) want(a *discord.Activity) {
	s.mu.Lock()
	s.wanted = a
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *sender) run(ctx context.Context) {
	var lastSend time.Time
	var healthy, failing bool // log only transitions, not every retry
	var rejected bool         // the last activity sent was rejected
	var lost bool             // Discord dropped what was sent; resend it
	for {
		s.mu.Lock()
		wanted := s.wanted
		pending := lost || !presence.Equal(wanted, s.sent, s.cfg.TimestampTolerance)
		s.mu.Unlock()

		if !pending {
			select {
			case <-ctx.Done():
				return
			case <-s.notify:
				continue
			case <-s.lost:
				// Discord forgot the activity: fine if it was cleared or rejected.
				if wanted != nil && !rejected {
					s.log.Info("discord connection lost; reconnecting")
					healthy, lost = false, true
				}
				continue
			}
		} else if wait := time.Until(lastSend.Add(s.cfg.MinUpdateInterval)); wait > 0 {
			if !sleep(ctx, wait) {
				return
			}
			continue // pick up whatever is wanted by now
		}

		err := s.presence.SetActivity(ctx, wanted)
		lastSend = time.Now()
		if errors.Is(err, discord.ErrRejected) {
			s.log.Error("discord rejected the activity; waiting for the next change", "err", err, "activity", wanted)
			healthy, failing, rejected, lost = false, false, true, false
			s.mu.Lock()
			s.sent = wanted
			s.mu.Unlock()
			continue
		}
		if err != nil {
			if !failing {
				s.log.Warn("discord update failed; retrying", "err", err, "every", s.cfg.RetryInterval)
			}
			healthy, failing = false, true
			if !sleep(ctx, s.cfg.RetryInterval) {
				return
			}
			continue
		}
		if !healthy {
			s.log.Info("discord updated")
		}
		healthy, failing, rejected, lost = true, false, false, false
		s.mu.Lock()
		s.sent = wanted
		s.mu.Unlock()
	}
}

// sleep waits for d and reports false if ctx was done first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
