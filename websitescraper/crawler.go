package websitescraper

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// contactPaths is the list of sub-paths we probe after the landing page.
// Ordered from most likely to least, across the major Western European
// languages plus generic business conventions. We stop as soon as we have
// enough signal (at least one email) or maxPages has been reached.
var contactPaths = []string{
	// Primary contact pages (most common).
	"contact",
	"contact-us",
	"contactus",
	"contacts",
	"nous-contacter",
	"kontakt",
	"contatti",
	"contacto",

	// About pages that usually carry a contact block.
	"about",
	"about-us",
	"a-propos",
	"qui-sommes-nous",
	"ueber-uns",
	"chi-siamo",

	// Legal pages — mandated email/phone disclosures in many EU countries.
	"legal",
	"mentions-legales",
	"impressum",
	"aviso-legal",

	// Team pages often list staff emails directly.
	"team",
	"notre-equipe",

	// Generic fallback.
	"support",
	"help",
}

// Options tunes the crawl. All fields are optional; zero values use sensible
// defaults.
type Options struct {
	// MaxPages caps the total number of HTTP fetches for one site (including
	// the landing page). Default 6.
	MaxPages int
	// PerPageTimeout is the per-request timeout passed to the Fetcher.
	PerPageTimeout time.Duration
	// InterPageDelay jitter-bounded sleep between page fetches so the crawl
	// looks less like a scanner. Default 300 ms.
	InterPageDelay time.Duration
	// StopOnFirstEmail short-circuits the crawl as soon as we have at least
	// one email (most use cases only need one reachable address per site).
	StopOnFirstEmail bool
}

// Crawl retrieves the landing page plus a handful of well-known contact
// sub-paths, runs the analyser on every response, and returns the merged
// ContactProfile.
//
// Failures on sub-pages are intentionally swallowed — we always try to
// return as much data as we could gather, even if the main page errored.
func Crawl(ctx context.Context, f *Fetcher, rawURL string, opts Options) (*ContactProfile, error) {
	if opts.MaxPages <= 0 {
		opts.MaxPages = 6
	}

	if opts.PerPageTimeout > 0 {
		f.SetTimeout(opts.PerPageTimeout)
	}

	if opts.InterPageDelay == 0 {
		opts.InterPageDelay = 300 * time.Millisecond
	}

	base, err := url.Parse(rawURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		// Try prepending https:// and retry once — Google Maps websites are
		// frequently listed as "example.com" with no scheme.
		if !strings.Contains(rawURL, "://") {
			base, err = url.Parse("https://" + rawURL)
		}

		if err != nil || base == nil || base.Host == "" {
			return nil, err
		}
	}

	profile := &ContactProfile{SocialLinks: map[string]string{}}
	visited := map[string]bool{}
	pages := 0

	fetchAndAnalyse := func(u string) {
		if pages >= opts.MaxPages || visited[u] {
			return
		}

		visited[u] = true
		pages++

		resp, err := f.Get(ctx, u)
		if err != nil || resp == nil || len(resp.Body) == 0 {
			return
		}

		final := resp.FinalURL
		if final == "" {
			final = u
		}

		Analyse(resp.Body, final, profile)
	}

	// Landing page.
	fetchAndAnalyse(base.String())

	// Follow discovered links from the landing page first — any real <a>
	// that points to "/contact" or similar on the SAME host is almost
	// always more accurate than our guessed paths.
	if pages < opts.MaxPages {
		for _, link := range discoveredContactLinks(profile, base) {
			if opts.StopOnFirstEmail && len(profile.Emails) > 0 {
				break
			}

			fetchAndAnalyse(link)

			sleepWithCtx(ctx, opts.InterPageDelay)
		}
	}

	// Guessed contact paths.
	for _, p := range contactPaths {
		if pages >= opts.MaxPages {
			break
		}

		if opts.StopOnFirstEmail && len(profile.Emails) > 0 {
			break
		}

		u := *base
		u.Path = "/" + p
		u.RawQuery = ""
		u.Fragment = ""

		fetchAndAnalyse(u.String())

		sleepWithCtx(ctx, opts.InterPageDelay)
	}

	return profile, nil
}

// discoveredContactLinks is populated by a side-scan of the source URL's
// anchors. We expose it as a hook so future heuristics can plug in without
// changing the crawler API.
//
// For now we only look at the accumulated social_links + source_urls. A
// full anchor scan happens inside Analyse, but we do not yet round-trip
// those anchors; leave this as a no-op stub so we can improve later
// without changing the crawl loop.
func discoveredContactLinks(_ *ContactProfile, _ *url.URL) []string {
	return nil
}

func sleepWithCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
