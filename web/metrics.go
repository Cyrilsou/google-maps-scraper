package web

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/webhook"
	"github.com/gosom/google-maps-scraper/websitescraper"
)

// metrics serves a Prometheus-compatible exposition of internal counters.
// We hand-roll the exposition format instead of pulling in prometheus/client_golang
// — the metric set is tiny, static, and the format is trivial (one line per
// metric).
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	blocks, ok := gmaps.DefaultProxyStats.Totals()
	pageTracked, pageLRU := gmapsPageFlagsStats()

	var b strings.Builder

	writeCounter := func(name, help string, value int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
			name, help, name, name, value)
	}

	writeGauge := func(name, help string, value int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n",
			name, help, name, name, value)
	}

	// --- Scraper outcomes
	writeCounter("gmaps_scrape_ok_total",
		"Total successful scrapes (URLs that produced a valid page).", ok)
	writeCounter("gmaps_scrape_blocked_total",
		"Total responses flagged as Google block / sorry / CAPTCHA pages.", blocks)

	if gmaps.DefaultAutoCooldown != nil && gmaps.DefaultAutoCooldown.Active() {
		writeGauge("gmaps_cooldown_active", "1 when auto-cooldown is currently pausing scrapes.", 1)
		writeGauge("gmaps_cooldown_remaining_seconds",
			"Seconds until the current cooldown ends.",
			int64(gmaps.DefaultAutoCooldown.Remaining().Seconds()))
	} else {
		writeGauge("gmaps_cooldown_active", "1 when auto-cooldown is currently pausing scrapes.", 0)
		writeGauge("gmaps_cooldown_remaining_seconds",
			"Seconds until the current cooldown ends.", 0)
	}

	// --- Memory / cache footprints
	writeGauge("gmaps_page_flags_size", "Number of tracked Playwright pages.", int64(pageTracked))
	writeGauge("gmaps_page_flags_lru_size", "LRU size for tracked pages.", int64(pageLRU))
	writeGauge("websitescraper_mx_cache_size",
		"Number of domains memoised by the MX validator.",
		int64(websitescraper.MXCacheSize()))
	writeGauge("websitescraper_rdap_cache_size",
		"Number of domains memoised by the RDAP client.",
		int64(websitescraper.RDAPCacheSize()))

	// --- Webhook delivery
	writeCounter("webhook_sent_total", "Webhook payloads successfully delivered.", webhook.DefaultMetrics.Sent.Load())
	writeCounter("webhook_failed_total", "Webhook payloads dropped after retries.", webhook.DefaultMetrics.Failed.Load())
	writeCounter("webhook_dropped_total", "Webhook payloads dropped because the queue was full.", webhook.DefaultMetrics.Dropped.Load())
	writeGauge("webhook_queue_high_water", "High-water mark of the webhook queue depth.", webhook.DefaultMetrics.QueueHigh.Load())

	_, _ = io.WriteString(w, b.String())
}

// gmapsPageFlagsStats is a tiny bridge into the gmaps package's unexported
// counter. We keep it in the web layer so /metrics does not force gmaps to
// export an internal helper.
func gmapsPageFlagsStats() (int, int) {
	// Delegate: gmaps exposes this via a public wrapper below.
	return gmaps.PageFlagsStats()
}
