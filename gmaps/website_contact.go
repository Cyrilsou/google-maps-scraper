package gmaps

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gosom/google-maps-scraper/websitescraper"
)

// defaultWebsiteFetcher is the process-wide Fetcher used to enrich Entries
// with off-Maps contact data. Lazy-initialised so the TLS session (and any
// FlareSolverr handshake) only happen when the feature is actually used.
var (
	websiteFetcher     *websitescraper.Fetcher
	websiteFetcherOnce sync.Once
)

func getWebsiteFetcher() *websitescraper.Fetcher {
	websiteFetcherOnce.Do(func() {
		websiteFetcher = websitescraper.NewFetcher()
	})

	return websiteFetcher
}

// EnrichWebsiteContact crawls the business website and fills e.Emails +
// e.WebsiteContact with the data we could find. Safe to call on an Entry
// that has no website — it is a no-op in that case.
//
// The crawl is bounded: at most crawlMaxPages fetches, crawlTimeout total
// wall time. Errors are intentionally swallowed because enrichment should
// never fail a scrape job that otherwise succeeded.
func EnrichWebsiteContact(ctx context.Context, e *Entry) {
	if e == nil {
		return
	}

	rawURL := strings.TrimSpace(e.WebSite)
	if rawURL == "" {
		return
	}

	if !e.IsWebsiteValidForEmail() {
		return
	}

	// Respect GMAPS_DISABLE_WEBSITE_SCRAPE for operators who want to run
	// only the Maps half of the pipeline.
	if os.Getenv("GMAPS_DISABLE_WEBSITE_SCRAPE") == "1" {
		return
	}

	const (
		crawlMaxPages = 6
		crawlTimeout  = 45 * time.Second
	)

	cctx, cancel := context.WithTimeout(ctx, crawlTimeout)
	defer cancel()

	profile, err := websitescraper.Crawl(cctx, getWebsiteFetcher(), rawURL, websitescraper.Options{
		MaxPages:         crawlMaxPages,
		PerPageTimeout:   15 * time.Second,
		InterPageDelay:   300 * time.Millisecond,
		StopOnFirstEmail: false,
	})
	if err != nil || profile == nil {
		return
	}

	if len(profile.Emails) > 0 {
		// Union: keep any Maps-derived emails first (usually the official
		// business address), then add anything the crawl discovered.
		seen := map[string]bool{}
		merged := []string{}

		for _, e := range e.Emails {
			s := strings.ToLower(strings.TrimSpace(e))
			if s == "" || seen[s] {
				continue
			}

			seen[s] = true
			merged = append(merged, s)
		}

		for _, e := range profile.Emails {
			if !seen[e] {
				seen[e] = true
				merged = append(merged, e)
			}
		}

		e.Emails = merged
	}

	e.WebsiteContact = contactFromProfile(profile)
}

func contactFromProfile(p *websitescraper.ContactProfile) *WebsiteContact {
	if p == nil {
		return nil
	}

	c := &WebsiteContact{
		Emails:         append([]string(nil), p.Emails...),
		Phones:         append([]string(nil), p.Phones...),
		SocialLinks:    map[string]string{},
		HasContactForm: p.HasContactForm,
		SourceURLs:     append([]string(nil), p.SourceURLs...),
	}

	for k, v := range p.SocialLinks {
		c.SocialLinks[k] = v
	}

	if p.Organization != nil {
		c.OrgName = p.Organization.Name
		c.OrgLegalName = p.Organization.LegalName
		c.OrgTelephone = p.Organization.Telephone
		c.OrgVATID = p.Organization.VATID
		c.OrgFoundingDate = p.Organization.FoundingDate
		c.OrgNumEmployees = p.Organization.NumEmployees
		c.OrgAddress = p.Organization.Address
		c.OrgSameAs = append([]string(nil), p.Organization.SameAs...)
	}

	return c
}
