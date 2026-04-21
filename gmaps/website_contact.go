package gmaps

import (
	"context"
	"net/url"
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

	// MX-filter everything we found before we commit to the export. An email
	// whose domain has no MX is almost always a false positive (image asset
	// we misparsed, WordPress placeholder, developer sample).
	if len(profile.Emails) > 0 {
		profile.Emails = websitescraper.ValidateEmails(cctx, profile.Emails)
	}

	if len(profile.Emails) > 0 {
		// Union: keep any Maps-derived emails first (usually the official
		// business address), then add anything the crawl discovered.
		seen := map[string]bool{}
		merged := []string{}

		for _, em := range e.Emails {
			s := strings.ToLower(strings.TrimSpace(em))
			if s == "" || seen[s] {
				continue
			}

			seen[s] = true
			merged = append(merged, s)
		}

		for _, em := range profile.Emails {
			if !seen[em] {
				seen[em] = true
				merged = append(merged, em)
			}
		}

		e.Emails = merged
	}

	contact := contactFromProfile(profile)

	// Tech stack: run the signatures over the landing page body. We do not
	// store per-page bodies on the profile, so we refetch the landing here;
	// it is almost always cached in the Fetcher's session.
	if techs := detectTech(cctx, rawURL); len(techs) > 0 {
		contact.TechStack = techs
	}

	// Guess canonical emails when we have a person's name but no direct
	// address. Avoids emitting garbage: each guess is MX-validated before
	// being exported.
	if len(e.Emails) == 0 && profile.Organization != nil && profile.Organization.Name != "" {
		if host := hostFromURL(rawURL); host != "" {
			guesses := websitescraper.GuessEmails(cctx, profile.Organization.Name, host)
			contact.GuessedEmails = guesses
		}
	}

	// Domain age + registrar via RDAP (24 h cache).
	if host := hostFromURL(rawURL); host != "" {
		if info := websitescraper.LookupDomain(cctx, host); info != nil && info.AgeYears > 0 {
			contact.DomainAgeYears = info.AgeYears
			contact.DomainRegistrar = info.Registrar
		}
	}

	// Normalise phones to E.164 using the entry's country as the default
	// hint — makes the output directly importable into CRMs that expect
	// canonical formatting. We normalise the top-level Phone field too so
	// the CSV/XLSX "phone" column is consistent with website_phones.
	country := countryISO2FromEntry(e)
	contact.Phones = websitescraper.NormaliseE164Slice(contact.Phones, country)

	if e.Phone != "" {
		if n := websitescraper.NormaliseE164(e.Phone, country); n != "" {
			e.Phone = n
		}
	}

	e.WebsiteContact = contact
}

// countryISO2FromEntry does a coarse lookup of the user-facing country name
// Google returns in CompleteAddress.Country. This is good enough to pick
// the right trunk prefix for E.164 normalisation on >95% of Maps results.
// Unknown countries return "" and the normaliser falls back to "+<digits>".
func countryISO2FromEntry(e *Entry) string {
	if e == nil {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(e.CompleteAddress.Country)) {
	case "france", "fr":
		return "FR"
	case "germany", "deutschland", "de":
		return "DE"
	case "united kingdom", "uk", "great britain", "england", "scotland", "wales":
		return "GB"
	case "italy", "italia", "it":
		return "IT"
	case "spain", "españa", "es":
		return "ES"
	case "portugal", "pt":
		return "PT"
	case "belgium", "belgique", "belgië", "be":
		return "BE"
	case "netherlands", "nederland", "nl":
		return "NL"
	case "switzerland", "suisse", "schweiz", "ch":
		return "CH"
	case "austria", "österreich", "at":
		return "AT"
	case "united states", "united states of america", "usa", "us":
		return "US"
	case "canada", "ca":
		return "CA"
	case "mexico", "méxico", "mx":
		return "MX"
	case "brazil", "brasil", "br":
		return "BR"
	case "argentina", "ar":
		return "AR"
	case "australia", "au":
		return "AU"
	case "new zealand", "nz":
		return "NZ"
	case "india", "in":
		return "IN"
	case "japan", "jp":
		return "JP"
	case "south korea", "korea, republic of", "republic of korea", "kr":
		return "KR"
	case "china", "cn":
		return "CN"
	case "ireland", "ie":
		return "IE"
	case "sweden", "sverige", "se":
		return "SE"
	case "norway", "norge", "no":
		return "NO"
	case "denmark", "danmark", "dk":
		return "DK"
	case "finland", "suomi", "fi":
		return "FI"
	case "poland", "polska", "pl":
		return "PL"
	case "greece", "gr":
		return "GR"
	case "morocco", "ma":
		return "MA"
	case "south africa", "za":
		return "ZA"
	}

	return ""
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	return strings.TrimPrefix(u.Host, "www.")
}

// detectTech re-fetches the landing page (cheap: cached TLS session) and
// runs the signatures. Returns nil on any error.
func detectTech(ctx context.Context, rawURL string) []TechItem {
	resp, err := getWebsiteFetcher().Get(ctx, rawURL)
	if err != nil || resp == nil {
		return nil
	}

	techs := websitescraper.DetectTech(resp.Body, resp.Headers)
	if len(techs) == 0 {
		return nil
	}

	out := make([]TechItem, 0, len(techs))
	for _, t := range techs {
		out = append(out, TechItem{Name: t.Name, Category: t.Category, Version: t.Version})
	}

	return out
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
