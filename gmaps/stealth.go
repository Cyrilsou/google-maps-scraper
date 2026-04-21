package gmaps

import (
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"
)

// ErrBlocked is returned when Google serves a CAPTCHA or "unusual traffic"
// interstitial instead of the expected page. Scrapemate treats this as a
// retryable error so the job can be re-queued on another proxy.
var ErrBlocked = errors.New("google maps: blocked (captcha / unusual traffic)")

// jitterMS returns base±30% in milliseconds. Fixed sleeps are a strong
// bot signal; every delay in the scraper is jittered through this helper.
func jitterMS(base int) int {
	if base <= 0 {
		return 0
	}

	spread := base * 3 / 10
	if spread <= 0 {
		return base
	}

	return base - spread + rand.IntN(2*spread+1)
}

// jitter returns base±30% as a time.Duration.
func jitter(base time.Duration) time.Duration {
	return time.Duration(jitterMS(int(base / time.Millisecond))) * time.Millisecond
}

// blockMarkers are substrings Google returns on CAPTCHA / rate-limit pages.
// The URL check (for /sorry/ and /httpservice/retry/enablejs) catches the
// redirect; the body check catches cases where the interstitial is served
// at 200 OK on the original URL.
var blockMarkers = []string{
	"g-recaptcha",
	"unusual traffic from your computer",
	"detected unusual traffic",
	"our systems have detected",
	"solve the above captcha",
	"/recaptcha/",
	"id=\"captcha-form\"",
}

// isBlockedResponse returns true when the URL or HTML body looks like a
// CAPTCHA / rate-limit / "sorry" page. Callers should turn this into
// ErrBlocked so the framework retries (ideally on a different proxy).
func isBlockedResponse(finalURL string, body []byte) bool {
	lowerURL := strings.ToLower(finalURL)
	if strings.Contains(lowerURL, "google.com/sorry/") ||
		strings.Contains(lowerURL, "/httpservice/retry/enablejs") {
		return true
	}

	if len(body) == 0 {
		return false
	}

	// Only scan the first 32 KB - the markers always appear near the top
	// of the interstitial and this avoids pathological cost on large pages.
	const scanCap = 32 * 1024

	sample := body
	if len(sample) > scanCap {
		sample = sample[:scanCap]
	}

	lower := strings.ToLower(string(sample))
	for _, m := range blockMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}

	return false
}

// Tracker / analytics domains that a real user session would fetch, but
// that add zero value for scraping. Blocking them cuts 15-30 % off the
// time-to-content for each place page. All the domains below belong to
// services Google itself marks as third-party in Chrome DevTools, so
// blocking them does not look suspicious.
var blockedResourceHosts = []string{
	"googletagmanager.com",
	"google-analytics.com",
	"googlesyndication.com",
	"doubleclick.net",
	"adservice.google.com",
	"pagead2.googlesyndication.com",
	"stats.g.doubleclick.net",
	"www.googleadservices.com",
}

// Resource types Playwright can cheaply abort. "font" and "media" are safe
// to block on Google Maps; "stylesheet" is NOT safe — the feed layout depends
// on it. We intentionally leave stylesheet alone.
var blockedResourceTypes = map[string]struct{}{
	"font":  {},
	"media": {},
	"other": {},
}

// pageFlags tracks per-page state that must not be re-initialised across
// reused pages: route handlers, init scripts, warmup navigations. The map
// is bounded via a simple LRU-style trim: when it grows past pageFlagsMax,
// the oldest entries are evicted. Keys are playwright.Page pointers, so
// stale entries only exist when Playwright fails to call our OnClose hook
// (rare but possible on process kill).
//
// One map with bit flags is cheaper than three separate maps and makes
// eviction consistent.
type pageFlag uint8

const (
	flagRouted pageFlag = 1 << iota
	flagFingerprinted
	flagWarmed
)

const pageFlagsMax = 512 // covers WithBrowserReuseLimit(200) × small buffer

var (
	pageFlagsMu sync.Mutex
	pageFlags   = map[playwright.Page]pageFlag{}
	pageFlagsLRU []playwright.Page
)

func setPageFlag(p playwright.Page, flag pageFlag) bool {
	if p == nil {
		return false
	}

	pageFlagsMu.Lock()

	current := pageFlags[p]
	if current&flag != 0 {
		pageFlagsMu.Unlock()

		return false
	}

	firstInsert := false
	if _, existed := pageFlags[p]; !existed {
		// Evict oldest if the map would overflow. 1 eviction per insert
		// is enough: we never insert in bursts.
		if len(pageFlagsLRU) >= pageFlagsMax {
			oldest := pageFlagsLRU[0]
			pageFlagsLRU = pageFlagsLRU[1:]
			delete(pageFlags, oldest)
		}

		pageFlagsLRU = append(pageFlagsLRU, p)
		firstInsert = true
	}

	pageFlags[p] = current | flag
	pageFlagsMu.Unlock()

	// Register the cleanup outside the critical section. Playwright may
	// invoke the OnClose handler synchronously in edge cases (e.g. page
	// already closed), and that handler re-acquires pageFlagsMu — we
	// would deadlock the goroutine if the lock were still held.
	if firstInsert {
		p.OnClose(func(_ playwright.Page) {
			pageFlagsMu.Lock()
			delete(pageFlags, p)

			for i, entry := range pageFlagsLRU {
				if entry == p {
					pageFlagsLRU = append(pageFlagsLRU[:i], pageFlagsLRU[i+1:]...)
					break
				}
			}
			pageFlagsMu.Unlock()
		})
	}

	return true
}

// pageFlagsStats is exposed for tests and /metrics; returns (tracked, lru_size).
func pageFlagsStats() (int, int) {
	pageFlagsMu.Lock()
	defer pageFlagsMu.Unlock()

	return len(pageFlags), len(pageFlagsLRU)
}

// InstallStealthRouting attaches a Playwright route handler that aborts
// requests to tracker hosts and fonts. Safe to call multiple times for the
// same page: subsequent calls are no-ops.
//
// Errors are non-fatal — we swallow them here because routing is a best-effort
// optimisation and the scrape should proceed even on Playwright-internal
// quirks (e.g. reused-page teardown races).
func InstallStealthRouting(page scrapemate.BrowserPage) {
	if page == nil {
		return
	}

	pwPage, ok := page.Unwrap().(playwright.Page)
	if !ok || pwPage == nil {
		return
	}

	if !setPageFlag(pwPage, flagRouted) {
		return
	}

	_ = pwPage.Route("**/*", func(route playwright.Route) {
		req := route.Request()
		if req == nil {
			_ = route.Continue()
			return
		}

		resourceType := req.ResourceType()
		if _, drop := blockedResourceTypes[resourceType]; drop {
			_ = route.Abort()
			return
		}

		reqURL := strings.ToLower(req.URL())
		for _, host := range blockedResourceHosts {
			if strings.Contains(reqURL, host) {
				_ = route.Abort()
				return
			}
		}

		_ = route.Continue()
	})
}

// fingerprintInitScript randomises the properties Google's fingerprinting
// scripts probe AFTER navigator.webdriver has already been spoofed by the
// stealth adapter. Each new page reuse cycle gets a fresh roll so the
// same IP does not always look like the same hardware.
//
// Values picked from the distribution of real Chrome installs on desktop.
// We avoid extremes (e.g. hardwareConcurrency=2 on a Windows laptop is now
// rare and looks like a VM).
func fingerprintInitScript() string {
	cores := []int{4, 6, 8, 8, 8, 12, 16}  // weighted toward 8
	memory := []int{4, 8, 8, 8, 16, 16, 32}
	timezones := []string{
		"Europe/Paris", "Europe/Berlin", "Europe/London", "Europe/Madrid",
		"Europe/Amsterdam", "America/New_York", "America/Chicago",
		"America/Los_Angeles",
	}
	widths := []int{1280, 1366, 1440, 1536, 1600, 1680, 1920, 2560}
	heights := map[int]int{
		1280: 720, 1366: 768, 1440: 900, 1536: 864,
		1600: 900, 1680: 1050, 1920: 1080, 2560: 1440,
	}

	c := cores[rand.IntN(len(cores))]
	m := memory[rand.IntN(len(memory))]
	tz := timezones[rand.IntN(len(timezones))]
	w := widths[rand.IntN(len(widths))]
	h := heights[w]

	// NB: we override the *value* returned; the property remains
	// configurable so Google's own code that writes to navigator still
	// works. `value` + `configurable:true` matches how puppeteer-stealth
	// does it.
	return `(() => {
		try {
			Object.defineProperty(navigator, 'hardwareConcurrency', { get: () => ` + itoa(c) + `, configurable: true });
			Object.defineProperty(navigator, 'deviceMemory',       { get: () => ` + itoa(m) + `, configurable: true });
			Object.defineProperty(screen,    'width',              { get: () => ` + itoa(w) + `, configurable: true });
			Object.defineProperty(screen,    'height',             { get: () => ` + itoa(h) + `, configurable: true });
			Object.defineProperty(screen,    'availWidth',         { get: () => ` + itoa(w) + `, configurable: true });
			Object.defineProperty(screen,    'availHeight',        { get: () => ` + itoa(h-40) + `, configurable: true });

			// Override Intl.DateTimeFormat().resolvedOptions().timeZone so
			// the timezone probe returns a consistent answer.
			const origResolved = Intl.DateTimeFormat.prototype.resolvedOptions;
			Intl.DateTimeFormat.prototype.resolvedOptions = function () {
				const r = origResolved.call(this);
				r.timeZone = '` + tz + `';
				return r;
			};

			// webdriver is already false via stealth adapter, but double-pin.
			Object.defineProperty(navigator, 'webdriver', { get: () => false, configurable: true });

			// Some sites probe permissions.query({name:'notifications'})
			// and flag "prompt" as suspicious; normalise to "default".
			const origQuery = navigator.permissions && navigator.permissions.query;
			if (origQuery) {
				navigator.permissions.query = (params) => {
					if (params && params.name === 'notifications') {
						return Promise.resolve({ state: Notification.permission || 'default' });
					}
					return origQuery.call(navigator.permissions, params);
				};
			}
		} catch (e) { /* ignored - never break the page */ }
	})();`
}

func itoa(n int) string {
	// Minimal inline conversion to avoid pulling strconv into stealth.
	if n == 0 {
		return "0"
	}

	neg := n < 0
	if neg {
		n = -n
	}

	var buf [20]byte
	i := len(buf)

	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}

	if neg {
		i--
		buf[i] = '-'
	}

	return string(buf[i:])
}

// InstallFingerprintShim registers fingerprintInitScript() on page so it
// runs on every navigation. Best-effort: no-op if AddInitScript is not
// available from the underlying driver.
func InstallFingerprintShim(page scrapemate.BrowserPage) {
	if page == nil {
		return
	}

	pw, ok := page.Unwrap().(playwright.Page)
	if !ok || pw == nil {
		return
	}

	if !setPageFlag(pw, flagFingerprinted) {
		return
	}

	_ = pw.AddInitScript(playwright.Script{Content: playwright.String(fingerprintInitScript())})
}

// WarmupNavigation visits https://www.google.com/maps/ before the first
// deep URL is loaded on this page. A user never navigates straight to a
// place/search URL — they come from the maps root, which sets consent
// cookies, session storage and the referer chain. Skipping the warmup is a
// subtle bot tell. Called once per page across its lifetime.
//
// Errors are swallowed because this is best-effort: if the warmup itself
// gets blocked, the caller will hit the block on the real URL anyway and
// the existing ErrBlocked path handles it.
func WarmupNavigation(page scrapemate.BrowserPage) {
	if page == nil {
		return
	}

	pwPage, ok := page.Unwrap().(playwright.Page)
	if !ok || pwPage == nil {
		// Fall back to the scrapemate interface - still useful, just no
		// dedup across reuse.
		_, _ = page.Goto("https://www.google.com/maps/", scrapemate.WaitUntilDOMContentLoaded)
		page.WaitForTimeout(jitter(400 * time.Millisecond))

		return
	}

	if !setPageFlag(pwPage, flagWarmed) {
		return
	}

	_, _ = page.Goto("https://www.google.com/maps/", scrapemate.WaitUntilDOMContentLoaded)
	// Small idle time so the session cookies settle before the real query.
	page.WaitForTimeout(jitter(400 * time.Millisecond))
}

