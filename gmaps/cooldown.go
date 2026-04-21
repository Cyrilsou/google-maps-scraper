package gmaps

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/gosom/google-maps-scraper/log"
)

// AutoCooldown pauses outgoing scrape traffic when the recent block rate
// crosses a threshold. The idea: if Google just returned 5 CAPTCHA pages in
// the last 2 minutes, keep scraping harder is pointless — every new request
// burns another request against an already-flagged IP and compounds the
// problem. Sleeping for a minute lets the rate limiter's token bucket refill.
//
// This is intentionally simple: a ring of timestamps, protected by one
// mutex, plus a deadline until which Wait returns early. Any ScrapeWorker
// can consult DefaultAutoCooldown before starting a job and skip without
// losing the job — River will retry it per the normal retry policy.
type AutoCooldown struct {
	mu         sync.Mutex
	events     []time.Time
	threshold  int           // how many blocks in window triggers cooldown
	window     time.Duration // sliding-window size
	cooldown   time.Duration // how long to pause once triggered
	cooldownUntil time.Time
}

// DefaultAutoCooldown is the process-wide instance read by ScrapeWorker. The
// default tuning is conservative: 5 blocks in a 2-minute window → 90 s
// cooldown (jittered).
var DefaultAutoCooldown = NewAutoCooldown(5, 2*time.Minute, 90*time.Second)

// NewAutoCooldown constructs an AutoCooldown with explicit thresholds.
func NewAutoCooldown(threshold int, window, cooldown time.Duration) *AutoCooldown {
	if threshold <= 0 {
		threshold = 5
	}

	if window <= 0 {
		window = 2 * time.Minute
	}

	if cooldown <= 0 {
		cooldown = 90 * time.Second
	}

	return &AutoCooldown{
		threshold: threshold,
		window:    window,
		cooldown:  cooldown,
		events:    make([]time.Time, 0, threshold*2),
	}
}

// RecordBlock is called every time a scrape path surfaces ErrBlocked. If
// this block brings us over the threshold, the internal cooldown deadline
// is armed.
func (a *AutoCooldown) RecordBlock() {
	if a == nil {
		return
	}

	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	// Drop events that fell out of the window.
	cutoff := now.Add(-a.window)
	trimmed := a.events[:0]

	for _, t := range a.events {
		if t.After(cutoff) {
			trimmed = append(trimmed, t)
		}
	}

	a.events = append(trimmed, now)

	if len(a.events) >= a.threshold {
		// Jitter ±30% so every worker does not come out of cooldown at the
		// exact same instant.
		spread := int(a.cooldown) * 3 / 10
		delta := rand.IntN(2*spread+1) - spread
		pause := a.cooldown + time.Duration(delta)

		until := now.Add(pause)
		if until.After(a.cooldownUntil) {
			a.cooldownUntil = until

			log.Warn("auto-cooldown triggered",
				"recent_blocks", len(a.events),
				"window_s", int(a.window.Seconds()),
				"pause_s", int(pause.Seconds()),
			)
		}

		// Reset the event log so a single burst does not keep re-arming
		// the cooldown forever.
		a.events = a.events[:0]
	}
}

// Wait blocks until the cooldown (if any) expires or ctx is cancelled.
// Returns the amount of time slept. Safe to call even when no cooldown is
// active — it returns immediately with 0.
func (a *AutoCooldown) Wait(ctx context.Context) time.Duration {
	if a == nil {
		return 0
	}

	a.mu.Lock()
	remaining := time.Until(a.cooldownUntil)
	a.mu.Unlock()

	if remaining <= 0 {
		return 0
	}

	timer := time.NewTimer(remaining)
	defer timer.Stop()

	start := time.Now()

	select {
	case <-timer.C:
	case <-ctx.Done():
	}

	return time.Since(start)
}

// Active reports whether a cooldown is currently in effect.
func (a *AutoCooldown) Active() bool {
	if a == nil {
		return false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	return time.Now().Before(a.cooldownUntil)
}

// Remaining returns how long the current cooldown still has to run, or 0.
func (a *AutoCooldown) Remaining() time.Duration {
	if a == nil {
		return 0
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	r := time.Until(a.cooldownUntil)
	if r < 0 {
		return 0
	}

	return r
}
