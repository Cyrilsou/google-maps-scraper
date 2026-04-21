package websitescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DomainInfo is the slim subset of RDAP data we keep: registration date (if
// available) so callers can compute domain age, and the registrar name when
// the TLD's RDAP server exposes it.
type DomainInfo struct {
	Domain          string    `json:"domain"`
	RegistrationAt  time.Time `json:"registration_at,omitempty"`
	Registrar       string    `json:"registrar,omitempty"`
	AgeYears        int       `json:"age_years,omitempty"`
	Source          string    `json:"source,omitempty"` // rdap endpoint that answered
}

// rdapCache caches per-domain results. RDAP servers rate-limit aggressively
// (IANA recommends 5 req/min per IP) so a campaign over hundreds of unique
// domains MUST cache — otherwise most requests get 429'd after the first
// wave.
type rdapCacheEntry struct {
	info    *DomainInfo
	expires time.Time
}

var (
	rdapCacheMu sync.RWMutex
	rdapCache   = map[string]rdapCacheEntry{}
)

// rdapBaseByTLD maps a TLD to its well-known RDAP server. The IANA bootstrap
// registry lists hundreds of these; we hard-code the most common commercial
// TLDs to avoid an extra bootstrap round-trip. Unknown TLDs fall back to the
// IANA RDAP endpoint which redirects transparently.
var rdapBaseByTLD = map[string]string{
	"com":   "https://rdap.verisign.com/com/v1",
	"net":   "https://rdap.verisign.com/net/v1",
	"org":   "https://rdap.publicinterestregistry.org/rdap",
	"info":  "https://rdap.identitydigital.services/rdap",
	"biz":   "https://rdap.nic.biz",
	"io":    "https://rdap.identitydigital.services/rdap",
	"co":    "https://rdap.nic.co",
	"fr":    "https://rdap.nic.fr",
	"de":    "https://rdap.denic.de",
	"uk":    "https://rdap.nominet.uk",
	"it":    "https://rdap.nic.it",
	"es":    "https://rdap.nic.es",
	"nl":    "https://rdap.dns.nl",
	"be":    "https://rdap.dns.be",
	"ch":    "https://rdap.nic.ch",
	"at":    "https://rdap.nic.at",
	"pt":    "https://rdap.dns.pt",
	"ca":    "https://rdap.ca.fury.ca/rdap",
	"app":   "https://rdap.nic.google",
	"dev":   "https://rdap.nic.google",
	"shop":  "https://rdap.nic.shop",
	"store": "https://rdap.centralnic.com/store",
	"xyz":   "https://rdap.centralnic.com/xyz",
}

// LookupDomain returns the best-effort RDAP snapshot for host. The lookup is
// cached for 24 hours; failures are cached for 30 minutes so a single 429
// does not block the same domain all day.
//
// Always safe to call — returns a non-nil *DomainInfo with only Domain set
// on error so downstream code does not need to nil-check.
func LookupDomain(ctx context.Context, host string) *DomainInfo {
	domain := cleanDomain(host)
	if domain == "" {
		return &DomainInfo{Domain: host}
	}

	rdapCacheMu.RLock()
	if ent, ok := rdapCache[domain]; ok && time.Now().Before(ent.expires) {
		rdapCacheMu.RUnlock()
		if ent.info != nil {
			return ent.info
		}
		return &DomainInfo{Domain: domain}
	}
	rdapCacheMu.RUnlock()

	info := queryRDAP(ctx, domain)

	ttl := 24 * time.Hour
	if info == nil || info.RegistrationAt.IsZero() {
		ttl = 30 * time.Minute
	}

	rdapCacheMu.Lock()
	rdapCache[domain] = rdapCacheEntry{info: info, expires: time.Now().Add(ttl)}
	rdapCacheMu.Unlock()

	if info == nil {
		return &DomainInfo{Domain: domain}
	}

	return info
}

// cleanDomain strips scheme, path, port, www prefix.
func cleanDomain(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}

	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	host := u.Hostname()
	host = strings.TrimPrefix(host, "www.")

	return host
}

func queryRDAP(ctx context.Context, domain string) *DomainInfo {
	dot := strings.LastIndex(domain, ".")
	if dot < 0 {
		return nil
	}

	tld := domain[dot+1:]

	base, ok := rdapBaseByTLD[tld]
	if !ok {
		base = "https://rdap.iana.org"
	}

	reqURL := base + "/domain/" + url.PathEscape(domain)

	// Tight timeout — RDAP is supposed to be a sub-second call.
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(lookupCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil
	}

	req.Header.Set("Accept", "application/rdap+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil
	}

	return parseRDAP(domain, body, base)
}

// rdapPayload captures just the fields we care about from the RDAP JSON
// (draft-ietf-regext-rdap-answers).
type rdapPayload struct {
	Events []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
	Entities []struct {
		Roles  []string        `json:"roles"`
		VCard  json.RawMessage `json:"vcardArray"`
	} `json:"entities"`
}

func parseRDAP(domain string, body []byte, source string) *DomainInfo {
	var p rdapPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}

	info := &DomainInfo{Domain: domain, Source: source}

	// Registration date lives in the "registration" event.
	for _, e := range p.Events {
		if strings.EqualFold(e.Action, "registration") {
			if t, err := time.Parse(time.RFC3339, e.Date); err == nil {
				info.RegistrationAt = t
				info.AgeYears = int(time.Since(t).Hours() / (24 * 365.25))
			}

			break
		}
	}

	// Registrar is an entity with roles containing "registrar". vcardArray
	// is a deeply nested jCard; we just look for the FN (full name) property.
	for _, ent := range p.Entities {
		isRegistrar := false

		for _, r := range ent.Roles {
			if strings.EqualFold(r, "registrar") {
				isRegistrar = true
				break
			}
		}

		if !isRegistrar || len(ent.VCard) == 0 {
			continue
		}

		if name := extractVCardFN(ent.VCard); name != "" {
			info.Registrar = name
			break
		}
	}

	return info
}

// extractVCardFN finds the first "fn" property in a jCard vcardArray. Signature
// pulled from RFC 7095 examples:
//   ["vcard", [ ["version",{},"text","4.0"], ["fn",{},"text","ACME Registrar"], ... ] ]
func extractVCardFN(raw json.RawMessage) string {
	var outer []json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer) < 2 {
		return ""
	}

	var entries [][]json.RawMessage
	if err := json.Unmarshal(outer[1], &entries); err != nil {
		return ""
	}

	for _, entry := range entries {
		if len(entry) < 4 {
			continue
		}

		var key string
		if err := json.Unmarshal(entry[0], &key); err != nil {
			continue
		}

		if !strings.EqualFold(key, "fn") {
			continue
		}

		var value string
		if err := json.Unmarshal(entry[3], &value); err == nil {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

// RDAPCacheSize reports the number of cached domains.
func RDAPCacheSize() int {
	rdapCacheMu.RLock()
	defer rdapCacheMu.RUnlock()

	return len(rdapCache)
}

// _ keeps fmt imported in case we extend error logging later.
var _ = fmt.Sprintf
