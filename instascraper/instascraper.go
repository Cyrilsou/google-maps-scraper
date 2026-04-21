// Package instascraper extracts public data from an Instagram profile page
// without logging in.
//
// The approach is deliberately conservative: Instagram serves rich Open
// Graph meta tags on /{handle}/ even to logged-out clients, and most of
// what we want for lead enrichment (name, bio, external URL, follower
// count, post count, verified / business flags, profile picture) is in
// those tags plus a small amount of initial-state JSON that survives in
// the HTML regardless of login-wall variants.
//
// Anti-bot:
//   - Same azuretls session as the rest of websitescraper: TLS ClientHello
//     and HTTP/2 frame order are replayed from real Chrome, so Instagram
//     does not immediately 401-lite us.
//   - FlareSolverr fallback (FLARESOLVERR_URL) when a Cloudflare / Meta
//     interstitial slips through.
//   - Polite defaults: one request per call, no follower list enumeration,
//     no hashtag scraping — we only look at the profile page itself.
//
// Not supported (by design):
//   - Private profiles (nothing public to scrape).
//   - Individual posts, reels, stories.
//   - Follower / following lists.
//   - Login-gated GraphQL queries (would require a session cookie that is
//     trivially banned).
package instascraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/gosom/google-maps-scraper/websitescraper"
)

// Profile is the slim subset of IG profile data we surface to callers.
// All fields are optional — Instagram keeps changing how much they
// render anonymously, so we never hard-fail on missing fields.
type Profile struct {
	Handle         string `json:"handle"`
	FullName       string `json:"full_name,omitempty"`
	Bio            string `json:"bio,omitempty"`
	ExternalURL    string `json:"external_url,omitempty"`
	ProfilePicture string `json:"profile_picture,omitempty"`
	FollowerCount  int    `json:"follower_count,omitempty"`
	FollowingCount int    `json:"following_count,omitempty"`
	PostCount      int    `json:"post_count,omitempty"`
	IsVerified     bool   `json:"is_verified,omitempty"`
	IsBusiness     bool   `json:"is_business,omitempty"`
	Category       string `json:"category,omitempty"`
}

// ErrNotFound is returned when the handle does not exist (404) or the
// profile page returned no parseable data.
var ErrNotFound = errors.New("instascraper: profile not found")

// FetchProfile resolves handleOrURL to an Instagram handle and fetches
// the public profile page. Handles can be given as:
//   - "username"
//   - "@username"
//   - "instagram.com/username"
//   - "https://www.instagram.com/username/"
//   - any URL whose first path segment is the handle
//
// fetcher is optional; when nil, a new default (with FlareSolverr from env)
// is created. Callers with an existing Fetcher (e.g. the website-scraper
// enrichment loop) should pass it in so cookies and TLS sessions are
// reused across crawls.
func FetchProfile(ctx context.Context, fetcher *websitescraper.Fetcher, handleOrURL string) (*Profile, error) {
	handle := extractHandle(handleOrURL)
	if handle == "" {
		return nil, fmt.Errorf("invalid instagram handle: %q", handleOrURL)
	}

	if fetcher == nil {
		fetcher = websitescraper.NewFetcher()
		defer fetcher.Close()
	}

	pageURL := "https://www.instagram.com/" + handle + "/"

	resp, err := fetcher.Get(ctx, pageURL)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}

	if len(resp.Body) == 0 {
		return nil, ErrNotFound
	}

	profile := &Profile{Handle: handle}

	// Start from the cheap, stable signal: OG meta tags. Works on every
	// layout variant IG ships.
	applyMetaTags(resp.Body, profile)

	// Enrich with the richer SSR JSON when present. Non-fatal.
	applyEmbeddedJSON(resp.Body, profile)

	// Nothing stuck → treat as not-found rather than returning an empty
	// struct that downstream code might interpret as "valid but blank".
	if profile.FullName == "" && profile.Bio == "" &&
		profile.FollowerCount == 0 && profile.PostCount == 0 {
		return nil, ErrNotFound
	}

	return profile, nil
}

var (
	// Handle grammar: 1-30 chars, letters/digits/underscore/period, must
	// not start or end with a period, no double-periods. We do not enforce
	// every constraint — just the ones that matter for URL parsing.
	handleRE = regexp.MustCompile(`^[A-Za-z0-9_.]{1,30}$`)
)

// extractHandle normalises the many formats an operator might paste into a
// Maps scrape config (URL, "@handle", bare handle) to a single lowercase
// handle string. Returns "" when the input is obviously not a handle.
func extractHandle(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// "@handle" → "handle"
	s = strings.TrimPrefix(s, "@")

	// URL forms.
	if strings.Contains(s, "instagram.com") || strings.HasPrefix(s, "http") {
		if u, err := url.Parse(ensureScheme(s)); err == nil {
			path := strings.Trim(u.Path, "/")
			if path != "" {
				parts := strings.SplitN(path, "/", 2)
				s = parts[0]
			}
		}
	}

	s = strings.ToLower(s)

	// IG reserves a handful of reserved paths ("explore", "p", "reels",
	// etc.). Reject them so we do not fetch a non-profile page.
	switch s {
	case "explore", "p", "reel", "reels", "stories", "tv", "accounts",
		"direct", "web", "legal", "about":
		return ""
	}

	if !handleRE.MatchString(s) {
		return ""
	}

	return s
}

func ensureScheme(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}

	return "https://" + s
}

// applyMetaTags populates full-name, bio, profile picture, and — when
// Instagram renders the canonical description format — the three social
// counts (followers / following / posts).
func applyMetaTags(body []byte, p *Profile) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return
	}

	if title := metaContent(doc, "og:title"); title != "" {
		// og:title is usually "<Full Name> (@<handle>) • Instagram photos
		// and videos". Strip the trailing handle + fluff.
		p.FullName = strings.TrimSpace(strings.SplitN(title, " (@", 2)[0])
	}

	if img := metaContent(doc, "og:image"); img != "" {
		p.ProfilePicture = img
	}

	if desc := metaContent(doc, "og:description"); desc != "" {
		// Format: "<followers> Followers, <following> Following, <posts>
		// Posts - <Name> (@<handle>) on Instagram: "<bio>""
		parseCountsFromDescription(desc, p)
	}

	// Twitter card sometimes has the bio when og:description doesn't.
	if p.Bio == "" {
		if desc := metaContent(doc, "twitter:description"); desc != "" {
			if bio := extractBioFromDescription(desc); bio != "" {
				p.Bio = bio
			}
		}
	}
}

func metaContent(doc *goquery.Document, property string) string {
	// Try both the canonical <meta property="..."> and the Twitter variant
	// <meta name="...">.
	var content string

	doc.Find(fmt.Sprintf(`meta[property=%q]`, property)).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if v, ok := s.Attr("content"); ok && v != "" {
			content = v
			return false
		}
		return true
	})

	if content != "" {
		return content
	}

	doc.Find(fmt.Sprintf(`meta[name=%q]`, property)).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if v, ok := s.Attr("content"); ok && v != "" {
			content = v
			return false
		}
		return true
	})

	return content
}

// numRE matches IG-style abbreviated counts: "1,234", "1.2K", "3.4M", "42B".
// The trailing suffix is anchored to a word boundary so "1.2KFollowers"
// parses as "1.2K" but "1.2Kfoobar" does not accidentally pull "K" as a
// suffix for an unrelated token.
var numRE = regexp.MustCompile(`([0-9][0-9,.]*)\s*([KMBkmb])?\b`)

// parseAbbrev returns the integer value of the LAST abbreviated number
// found in s. Taking the last match (rather than the first) lets callers
// feed a sliding window like "12.3K Followers, 842 " and get 842 rather
// than 12300. Returns 0 when no number is present.
func parseAbbrev(s string) int {
	matches := numRE.FindAllStringSubmatch(strings.TrimSpace(s), -1)
	if len(matches) == 0 {
		return 0
	}

	m := matches[len(matches)-1]
	base := strings.ReplaceAll(m[1], ",", "")

	f, err := strconv.ParseFloat(base, 64)
	if err != nil {
		return 0
	}

	if len(m) > 2 {
		switch strings.ToUpper(m[2]) {
		case "K":
			f *= 1000
		case "M":
			f *= 1000000
		case "B":
			f *= 1000000000
		}
	}

	return int(f)
}

// parseCountsFromDescription handles both the English og:description format
// ("1,234 Followers, 56 Following, 78 Posts - ...") and the localised
// variants Instagram serves when the user-agent implies another locale.
func parseCountsFromDescription(desc string, p *Profile) {
	lower := strings.ToLower(desc)

	// Extract counts keyed off their label. We also accept the
	// abbreviated "1.2K Followers" format (IG uses it for big accounts).
	patterns := []struct {
		needles []string
		set     func(int)
	}{
		{[]string{"followers", "abonnés", "abonnes", "seguidores", "follower", "abonnenten"}, func(n int) { p.FollowerCount = n }},
		{[]string{"following", "abonnements", "seguindo", "folgt"}, func(n int) { p.FollowingCount = n }},
		{[]string{"posts", "publications", "publicaciones", "beiträge", "beitrage", "post"}, func(n int) { p.PostCount = n }},
	}

	for _, pat := range patterns {
		for _, needle := range pat.needles {
			if i := strings.Index(lower, needle); i > 0 {
				// Grab the 30 chars before the needle and look for the
				// last number in that window.
				from := i - 30
				if from < 0 {
					from = 0
				}

				if n := parseAbbrev(desc[from:i]); n > 0 {
					pat.set(n)
					break
				}
			}
		}
	}

	p.Bio = extractBioFromDescription(desc)
}

// extractBioFromDescription peels the bio out of the Instagram
// og:description template. Example:
//   "1,234 Followers, 56 Following, 78 Posts - See Instagram photos and
//    videos from John Doe (@johndoe)"
// We also handle the alternate:
//   "@johndoe on Instagram: \"<bio>\""
func extractBioFromDescription(desc string) string {
	// Pattern A: bio in quotes after "on Instagram:".
	if i := strings.Index(strings.ToLower(desc), "on instagram:"); i >= 0 {
		rest := strings.TrimSpace(desc[i+len("on instagram:"):])

		rest = strings.TrimPrefix(rest, "\"")
		rest = strings.TrimSuffix(rest, "\"")

		if rest != "" {
			return rest
		}
	}

	// Pattern B: there is no bio in the description; just return "".
	return ""
}

// applyEmbeddedJSON looks for the initial-state payload Instagram still
// embeds as a <script> JSON blob. The exact key has changed over the years
// ("window._sharedData", "require('__eb.ProfilePageContainer')",
// "window.__additionalDataLoaded"), so we try several regex-anchored
// extractions and keep the first that produces a sensible user object.
func applyEmbeddedJSON(body []byte, p *Profile) {
	text := string(body)

	// IG now ships a subset of profile data in the LDS-style
	// "xdt_api__v1__users__web_profile_info" key buried in a larger JSON
	// blob that is rendered inline. We locate the inner object by looking
	// for the canonical "biography" + "full_name" + "username" triple.
	type userFragment struct {
		Biography            string `json:"biography"`
		FullName             string `json:"full_name"`
		ExternalURL          string `json:"external_url"`
		IsVerified           bool   `json:"is_verified"`
		IsBusinessAccount    bool   `json:"is_business_account"`
		BusinessCategoryName string `json:"business_category_name"`
		CategoryName         string `json:"category_name"`
		ProfilePicURL        string `json:"profile_pic_url"`
		ProfilePicURLHD      string `json:"profile_pic_url_hd"`
		EdgeFollow           struct {
			Count int `json:"count"`
		} `json:"edge_follow"`
		EdgeFollowedBy struct {
			Count int `json:"count"`
		} `json:"edge_followed_by"`
		EdgeOwnerToTimelineMedia struct {
			Count int `json:"count"`
		} `json:"edge_owner_to_timeline_media"`
	}

	// Find candidate "user":{...} objects.
	for _, marker := range []string{`"user":{`, `"user": {`} {
		for i := 0; i < len(text); {
			idx := strings.Index(text[i:], marker)
			if idx < 0 {
				break
			}

			start := i + idx + len(marker) - 1
			end := findMatchingBrace(text, start)
			if end <= start {
				break
			}

			var u userFragment
			if err := json.Unmarshal([]byte(text[start:end+1]), &u); err == nil && (u.FullName != "" || u.Biography != "") {
				if p.FullName == "" {
					p.FullName = u.FullName
				}
				if p.Bio == "" {
					p.Bio = u.Biography
				}
				if p.ExternalURL == "" {
					p.ExternalURL = u.ExternalURL
				}
				if !p.IsVerified && u.IsVerified {
					p.IsVerified = true
				}
				if !p.IsBusiness && u.IsBusinessAccount {
					p.IsBusiness = true
				}
				if p.Category == "" {
					if u.BusinessCategoryName != "" {
						p.Category = u.BusinessCategoryName
					} else {
						p.Category = u.CategoryName
					}
				}
				if p.ProfilePicture == "" {
					if u.ProfilePicURLHD != "" {
						p.ProfilePicture = u.ProfilePicURLHD
					} else {
						p.ProfilePicture = u.ProfilePicURL
					}
				}
				if p.FollowerCount == 0 {
					p.FollowerCount = u.EdgeFollowedBy.Count
				}
				if p.FollowingCount == 0 {
					p.FollowingCount = u.EdgeFollow.Count
				}
				if p.PostCount == 0 {
					p.PostCount = u.EdgeOwnerToTimelineMedia.Count
				}

				return
			}

			i = end + 1
		}
	}
}

// findMatchingBrace returns the index of the '}' that closes the '{' at
// start, respecting strings. Returns -1 when no match is found.
func findMatchingBrace(s string, start int) int {
	if start >= len(s) || s[start] != '{' {
		return -1
	}

	depth := 0
	inString := false
	escape := false

	for i := start; i < len(s); i++ {
		c := s[i]

		if escape {
			escape = false
			continue
		}

		if c == '\\' {
			escape = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}
