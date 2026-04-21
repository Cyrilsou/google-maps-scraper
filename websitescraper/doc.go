// Package websitescraper enriches a gmaps.Entry with contact data extracted
// from the business's own website: emails (incl. de-obfuscated forms like
// "name [at] domain [dot] com"), phones, social links, schema.org Organization
// JSON-LD, and whether a contact form is present.
//
// # Usage (Go)
//
//	f := websitescraper.NewFetcher()
//	defer f.Close()
//	profile, _ := websitescraper.Crawl(ctx, f, "https://example.com", websitescraper.Options{MaxPages: 6})
//	fmt.Println(profile.Emails, profile.Phones)
//
// # Anti-bot
//
// The default Fetcher uses github.com/Noooste/azuretls-client which mimics
// the TLS ClientHello + HTTP/2 frame order of real Chrome, which alone is
// enough to pass through most Cloudflare "I'm Under Attack" and "managed
// challenge" screens.
//
// When a page is still served as a Cloudflare interstitial and the operator
// has a FlareSolverr instance running
// (https://github.com/FlareSolverr/FlareSolverr), the Fetcher retries through
// it automatically. Configure with an environment variable:
//
//	export FLARESOLVERR_URL=http://127.0.0.1:8191
//
// Disable website enrichment entirely (e.g. when you only want the Maps
// portion of the pipeline):
//
//	export GMAPS_DISABLE_WEBSITE_SCRAPE=1
//
// # What pages are crawled
//
// Landing page + a short ordered list of conventional paths (/contact,
// /about, /impressum, /mentions-legales, /kontakt, /aviso-legal, ...) up to
// MaxPages (default 6). A small jitter-bounded pause separates requests.
//
// # Output
//
// Crawl returns a *ContactProfile with de-duped, case-normalised emails and
// canonicalised phones. A gmaps.WebsiteContact convertor in the gmaps
// package flattens the profile onto the Entry for CSV/XLSX/JSONL export.
package websitescraper
