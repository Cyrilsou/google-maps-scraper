package websitescraper

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/mcnijman/go-emailaddress"
)

// ContactProfile is the rich set of contact signals we extract from one or
// more pages of the same site. It stacks additively across pages so a crawl
// over root + /contact returns the union.
type ContactProfile struct {
	Emails       []string          `json:"emails,omitempty"`
	Phones       []string          `json:"phones,omitempty"`
	SocialLinks  map[string]string `json:"social_links,omitempty"`
	HasContactForm bool            `json:"has_contact_form,omitempty"`
	Organization *Organization     `json:"organization,omitempty"`
	SourceURLs   []string          `json:"source_urls,omitempty"`
}

// Organization captures the fields of schema.org/Organization or LocalBusiness
// that businesses typically expose via JSON-LD.
type Organization struct {
	Name          string   `json:"name,omitempty"`
	LegalName     string   `json:"legal_name,omitempty"`
	Email         string   `json:"email,omitempty"`
	Telephone     string   `json:"telephone,omitempty"`
	FoundingDate  string   `json:"founding_date,omitempty"`
	VATID         string   `json:"vat_id,omitempty"`
	NumEmployees  string   `json:"num_employees,omitempty"`
	Address       string   `json:"address,omitempty"`
	SameAs        []string `json:"same_as,omitempty"`
}

// Analyse walks an HTML body and appends every contact signal it can find
// to dest. Idempotent; callers should use the returned *ContactProfile as
// the source of truth and drop any temporary copy they held.
func Analyse(body []byte, baseURL string, dest *ContactProfile) *ContactProfile {
	if dest == nil {
		dest = &ContactProfile{}
	}

	if len(body) == 0 {
		return dest
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return dest
	}

	addUnique(&dest.Emails, extractEmails(doc, body))
	addUnique(&dest.Phones, extractPhones(doc, body))

	if dest.SocialLinks == nil {
		dest.SocialLinks = map[string]string{}
	}

	for k, v := range extractSocialLinks(doc, baseURL) {
		if _, already := dest.SocialLinks[k]; !already {
			dest.SocialLinks[k] = v
		}
	}

	if !dest.HasContactForm {
		dest.HasContactForm = hasContactForm(doc)
	}

	if org := extractOrganization(doc); org != nil {
		dest.Organization = mergeOrg(dest.Organization, org)
	}

	if baseURL != "" {
		addUnique(&dest.SourceURLs, []string{baseURL})
	}

	return dest
}

// --- email extraction -------------------------------------------------------

// Obfuscations like "foo [at] bar [dot] com", "foo (at) bar.com" and
// HTML-encoded addresses are common on small business sites. We normalise
// them before handing off to the robust emailaddress.Find parser.
var (
	atRE      = regexp.MustCompile(`(?i)\s*[\[\(\{<]\s*(?:at|arobase|chez)\s*[\]\)\}>]\s*`)
	dotRE     = regexp.MustCompile(`(?i)\s*[\[\(\{<]\s*(?:dot|point|punkt)\s*[\]\)\}>]\s*`)
	spaceAtRE = regexp.MustCompile(`(?i)\s+(?:at|arobase|chez)\s+`)
	spaceDotRE = regexp.MustCompile(`(?i)\s+(?:dot|point|punkt)\s+`)
)

func normaliseObfuscations(s string) string {
	s = atRE.ReplaceAllString(s, "@")
	s = dotRE.ReplaceAllString(s, ".")
	s = spaceAtRE.ReplaceAllString(s, "@")
	s = spaceDotRE.ReplaceAllString(s, ".")

	return s
}

func extractEmails(doc *goquery.Document, body []byte) []string {
	seen := map[string]bool{}
	out := []string{}

	// 1. <a href="mailto:..."> — highest confidence source.
	doc.Find("a[href^='mailto:']").Each(func(_ int, s *goquery.Selection) {
		mailto, ok := s.Attr("href")
		if !ok {
			return
		}

		candidate := strings.TrimPrefix(mailto, "mailto:")
		// mailto: can carry ?subject=... parameters; strip them.
		if i := strings.IndexAny(candidate, "?&"); i != -1 {
			candidate = candidate[:i]
		}

		for _, part := range strings.Split(candidate, ",") {
			if e, err := emailaddress.Parse(strings.TrimSpace(part)); err == nil {
				addEmail(&out, seen, e.String())
			}
		}
	})

	// 2. Whole-body regex over the raw HTML, with de-obfuscation.
	searchable := append([]byte{}, body...)
	searchable = []byte(normaliseObfuscations(string(searchable)))

	for _, addr := range emailaddress.Find(searchable, false) {
		addEmail(&out, seen, addr.String())
	}

	// 3. Text nodes (goquery strips tags), in case the body had heavy
	//    encoding that Find missed.
	text := doc.Text()
	text = normaliseObfuscations(text)
	for _, addr := range emailaddress.Find([]byte(text), false) {
		addEmail(&out, seen, addr.String())
	}

	sort.Strings(out)

	return out
}

// addEmail keeps the list unique and filters the common garbage addresses
// (pixel tracking, placeholder, WordPress) that never belong in a lead.
var junkEmailHosts = []string{
	"sentry.io", "sentry-next.wixpress.com", "wixpress.com", "example.com",
	"example.org", "yourdomain.com", "domain.com",
}

var junkEmailLocals = []string{"noreply", "no-reply", "donotreply", "do-not-reply", "postmaster"}

func addEmail(out *[]string, seen map[string]bool, e string) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" || seen[e] {
		return
	}

	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return
	}

	local := e[:at]
	host := e[at+1:]

	for _, h := range junkEmailHosts {
		if host == h {
			return
		}
	}

	for _, l := range junkEmailLocals {
		if local == l {
			return
		}
	}

	// Images like foo@2x.png are a frequent false-positive.
	if strings.HasSuffix(e, ".png") || strings.HasSuffix(e, ".jpg") ||
		strings.HasSuffix(e, ".jpeg") || strings.HasSuffix(e, ".gif") ||
		strings.HasSuffix(e, ".svg") || strings.HasSuffix(e, ".webp") {
		return
	}

	seen[e] = true
	*out = append(*out, e)
}

// --- phone extraction -------------------------------------------------------

// phoneRE matches international (+33 …) and national long-form phone numbers.
// We deliberately avoid go-phonenumbers to keep the dependency footprint
// small; a regex + tel: harvest covers ~95% of small-business sites.
var phoneRE = regexp.MustCompile(`(?i)(\+[0-9][\d\s().\-]{7,20}[\d])|(\b0[\d][\d\s().\-]{7,18}[\d])`)

func extractPhones(doc *goquery.Document, body []byte) []string {
	seen := map[string]bool{}
	out := []string{}

	// 1. tel: links — highest precision.
	doc.Find("a[href^='tel:']").Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok {
			return
		}

		addPhone(&out, seen, strings.TrimPrefix(href, "tel:"))
	})

	// 2. Regex over the body text — catches plain-text numbers. Skip when
	//    we've already got three+ phones from tel: links: at that point
	//    the regex mostly produces noise.
	if len(out) >= 3 {
		sort.Strings(out)

		return out
	}

	text := doc.Text()
	for _, m := range phoneRE.FindAllString(text, -1) {
		addPhone(&out, seen, m)
	}

	sort.Strings(out)

	return out
}

func addPhone(out *[]string, seen map[string]bool, raw string) {
	n := normalisePhone(raw)
	if n == "" || seen[n] {
		return
	}

	// Must have at least 8 digits — anything shorter is a year, postal
	// code fragment, etc.
	digitCount := 0
	for _, r := range n {
		if r >= '0' && r <= '9' {
			digitCount++
		}
	}

	if digitCount < 8 || digitCount > 15 {
		return
	}

	seen[n] = true
	*out = append(*out, n)
}

// normalisePhone collapses whitespace, dashes, dots and parentheses into a
// single canonical spacing-free string (keeps the leading + for
// international form).
func normalisePhone(s string) string {
	s = strings.TrimSpace(s)

	var b strings.Builder

	for _, r := range s {
		switch {
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}

	return b.String()
}

// --- social links -----------------------------------------------------------

var socialHosts = map[string]string{
	"facebook.com":  "facebook",
	"fb.com":        "facebook",
	"twitter.com":   "twitter",
	"x.com":         "twitter",
	"instagram.com": "instagram",
	"linkedin.com":  "linkedin",
	"youtube.com":   "youtube",
	"youtu.be":      "youtube",
	"tiktok.com":    "tiktok",
	"pinterest.com": "pinterest",
	"github.com":    "github",
	"t.me":          "telegram",
	"wa.me":         "whatsapp",
	"api.whatsapp.com": "whatsapp",
}

func extractSocialLinks(doc *goquery.Document, baseURL string) map[string]string {
	out := map[string]string{}
	base, _ := url.Parse(baseURL)

	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if href == "" {
			return
		}

		u, err := url.Parse(href)
		if err != nil {
			return
		}

		if !u.IsAbs() && base != nil {
			u = base.ResolveReference(u)
		}

		host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
		if host == "" {
			return
		}

		if network, ok := socialHosts[host]; ok {
			// Prefer the first non-generic link we see (skip bare hosts).
			if u.Path == "" || u.Path == "/" {
				if _, already := out[network]; already {
					return
				}
			}

			if _, already := out[network]; !already {
				out[network] = u.String()
			}
		}
	})

	return out
}

// --- contact form detection --------------------------------------------------

func hasContactForm(doc *goquery.Document) bool {
	found := false

	// Plain <form> that points to /contact, contains an email field, or is
	// inside a section labelled "contact".
	doc.Find("form").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		action, _ := s.Attr("action")
		action = strings.ToLower(action)

		if strings.Contains(action, "contact") || strings.Contains(action, "message") ||
			strings.Contains(action, "mailto:") {
			found = true
			return false
		}

		if s.Find("input[type='email']").Length() > 0 {
			found = true
			return false
		}

		ids := strings.ToLower(s.AttrOr("id", "") + " " + s.AttrOr("class", ""))
		if strings.Contains(ids, "contact") || strings.Contains(ids, "message") {
			found = true
			return false
		}

		return true
	})

	return found
}

// --- JSON-LD organisation ---------------------------------------------------

func extractOrganization(doc *goquery.Document) *Organization {
	var found *Organization

	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		payload := strings.TrimSpace(s.Text())
		if payload == "" {
			return true
		}

		// JSON-LD blocks are often arrays or @graph containers; walk
		// everything to catch LocalBusiness/Organization nested anywhere.
		var root any
		if err := json.Unmarshal([]byte(payload), &root); err != nil {
			return true
		}

		if org := findOrganization(root); org != nil {
			found = org
			return false
		}

		return true
	})

	return found
}

func findOrganization(node any) *Organization {
	switch v := node.(type) {
	case []any:
		for _, item := range v {
			if org := findOrganization(item); org != nil {
				return org
			}
		}
	case map[string]any:
		if graph, ok := v["@graph"]; ok {
			if org := findOrganization(graph); org != nil {
				return org
			}
		}

		if t, ok := v["@type"]; ok && isOrgLike(t) {
			return buildOrganization(v)
		}
	}

	return nil
}

func isOrgLike(t any) bool {
	switch v := t.(type) {
	case string:
		return orgTypeMatch(v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && orgTypeMatch(s) {
				return true
			}
		}
	}

	return false
}

func orgTypeMatch(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "organization") ||
		strings.Contains(s, "localbusiness") ||
		strings.Contains(s, "corporation")
}

func buildOrganization(m map[string]any) *Organization {
	o := &Organization{}

	o.Name = getString(m, "name")
	o.LegalName = getString(m, "legalName")
	o.Email = getString(m, "email")
	o.Telephone = getString(m, "telephone")
	o.FoundingDate = getString(m, "foundingDate")
	o.VATID = getString(m, "vatID")
	o.NumEmployees = getString(m, "numberOfEmployees")

	if addr, ok := m["address"]; ok {
		o.Address = flattenAddress(addr)
	}

	if sa, ok := m["sameAs"]; ok {
		switch v := sa.(type) {
		case string:
			o.SameAs = []string{v}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					o.SameAs = append(o.SameAs, s)
				}
			}
		}
	}

	return o
}

func flattenAddress(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case map[string]any:
		parts := []string{}
		for _, k := range []string{"streetAddress", "postalCode", "addressLocality", "addressRegion", "addressCountry"} {
			if s := getString(v, k); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	}

	return ""
}

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}

	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		// e.g. numberOfEmployees can come as a number.
		return trimNumber(x)
	}

	return ""
}

func trimNumber(f float64) string {
	if f == float64(int64(f)) {
		return intToString(int64(f))
	}

	return ""
}

func intToString(v int64) string {
	// Avoid strconv dependency inline — minimal inlined conversion.
	if v == 0 {
		return "0"
	}

	neg := false
	if v < 0 {
		neg = true
		v = -v
	}

	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}

	if neg {
		i--
		buf[i] = '-'
	}

	return string(buf[i:])
}

func mergeOrg(dst, src *Organization) *Organization {
	if dst == nil {
		return src
	}

	if src == nil {
		return dst
	}

	if dst.Name == "" {
		dst.Name = src.Name
	}

	if dst.LegalName == "" {
		dst.LegalName = src.LegalName
	}

	if dst.Email == "" {
		dst.Email = src.Email
	}

	if dst.Telephone == "" {
		dst.Telephone = src.Telephone
	}

	if dst.FoundingDate == "" {
		dst.FoundingDate = src.FoundingDate
	}

	if dst.VATID == "" {
		dst.VATID = src.VATID
	}

	if dst.NumEmployees == "" {
		dst.NumEmployees = src.NumEmployees
	}

	if dst.Address == "" {
		dst.Address = src.Address
	}

	dst.SameAs = mergeStrings(dst.SameAs, src.SameAs)

	return dst
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}

	for _, s := range a {
		seen[s] = true
	}

	for _, s := range b {
		if !seen[s] {
			a = append(a, s)
			seen[s] = true
		}
	}

	return a
}

func addUnique(dst *[]string, src []string) {
	seen := map[string]bool{}

	for _, s := range *dst {
		seen[s] = true
	}

	for _, s := range src {
		if s == "" || seen[s] {
			continue
		}

		seen[s] = true
		*dst = append(*dst, s)
	}
}
