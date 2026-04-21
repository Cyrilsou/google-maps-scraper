package websitescraper

import (
	"regexp"
	"sort"
	"strings"
)

// Tech is one detected piece of the website's stack. The set is intentionally
// shorter than Wappalyzer's full database — we only keep signals that matter
// for lead qualification (CMS, e-commerce, payments, booking, analytics,
// chat). Adding more is a one-liner in the signatures list below.
type Tech struct {
	Name     string `json:"name"`
	Category string `json:"category"` // cms, ecommerce, payments, booking, analytics, chat, hosting
	Version  string `json:"version,omitempty"`
}

// techSignature declares how to detect one piece of tech. A match on any of
// the regexes (run against headers OR body) flags the tech. Exactly one of
// headerRE / bodyRE must be set; both are allowed and OR'd.
type techSignature struct {
	name     string
	category string
	// headerRE runs against a joined "Key: value" string of response headers.
	headerRE *regexp.Regexp
	// bodyRE runs against the HTML body (prefix, for speed).
	bodyRE *regexp.Regexp
	// versionFromBody extracts the version from bodyRE's first submatch when
	// it defines one.
	versionSubmatch int
}

var techSignatures = []techSignature{
	// --- CMS
	{name: "WordPress", category: "cms",
		headerRE: regexp.MustCompile(`(?i)x-powered-by: WordPress`),
		bodyRE:   regexp.MustCompile(`(?i)/wp-(content|includes)/|<meta name="generator" content="WordPress ?([0-9.]+)?`),
		versionSubmatch: 2},
	{name: "Shopify", category: "ecommerce",
		headerRE: regexp.MustCompile(`(?i)x-shopid|x-shopify-stage`),
		bodyRE:   regexp.MustCompile(`(?i)cdn\.shopify\.com|Shopify\.theme`)},
	{name: "Wix", category: "cms",
		headerRE: regexp.MustCompile(`(?i)x-wix-`),
		bodyRE:   regexp.MustCompile(`(?i)static\.wixstatic\.com|x-wix-request-id`)},
	{name: "Squarespace", category: "cms",
		bodyRE: regexp.MustCompile(`(?i)squarespace\.com|static1\.squarespace|This is Squarespace`)},
	{name: "Webflow", category: "cms",
		bodyRE: regexp.MustCompile(`(?i)webflow\.com/js|wf-eu\d|data-wf-page`)},
	{name: "Drupal", category: "cms",
		headerRE: regexp.MustCompile(`(?i)x-(drupal|generator): drupal`),
		bodyRE:   regexp.MustCompile(`(?i)<meta name="generator" content="drupal`)},
	{name: "Joomla", category: "cms",
		bodyRE: regexp.MustCompile(`(?i)<meta name="generator" content="joomla`)},
	{name: "Ghost", category: "cms",
		bodyRE: regexp.MustCompile(`(?i)<meta name="generator" content="ghost\s*([0-9.]+)?"`),
		versionSubmatch: 1},
	{name: "PrestaShop", category: "ecommerce",
		headerRE: regexp.MustCompile(`(?i)powered-by: prestashop`),
		bodyRE:   regexp.MustCompile(`(?i)prestashop|<meta name="generator" content="PrestaShop`)},
	{name: "Magento", category: "ecommerce",
		headerRE: regexp.MustCompile(`(?i)x-magento`),
		bodyRE:   regexp.MustCompile(`(?i)/static/version\d+/frontend/|Mage\.Cookies`)},
	{name: "WooCommerce", category: "ecommerce",
		bodyRE: regexp.MustCompile(`(?i)woocommerce|wp-content/plugins/woocommerce`)},
	{name: "BigCommerce", category: "ecommerce",
		bodyRE: regexp.MustCompile(`(?i)cdn\d*\.bigcommerce\.com|bigcommerce\.com`)},

	// --- Payments
	{name: "Stripe", category: "payments",
		bodyRE: regexp.MustCompile(`(?i)js\.stripe\.com|stripe-checkout|stripe-js`)},
	{name: "PayPal", category: "payments",
		bodyRE: regexp.MustCompile(`(?i)paypal\.com/sdk/js|paypalobjects\.com|www\.paypal\.com/buttons`)},
	{name: "Square", category: "payments",
		bodyRE: regexp.MustCompile(`(?i)js\.squareup\.com|squareup\.com/checkout`)},
	{name: "Klarna", category: "payments",
		bodyRE: regexp.MustCompile(`(?i)klarna\.com|klarnaonsite`)},

	// --- Booking / reservations
	{name: "Calendly", category: "booking",
		bodyRE: regexp.MustCompile(`(?i)assets\.calendly\.com|calendly-badge-widget`)},
	{name: "OpenTable", category: "booking",
		bodyRE: regexp.MustCompile(`(?i)opentable\.com/widget|otwidget`)},
	{name: "TheFork", category: "booking",
		bodyRE: regexp.MustCompile(`(?i)thefork\.com|lafourchette\.com`)},
	{name: "Doctolib", category: "booking",
		bodyRE: regexp.MustCompile(`(?i)doctolib\.(fr|de|it)`)},
	{name: "Resy", category: "booking",
		bodyRE: regexp.MustCompile(`(?i)widgets\.resy\.com|resy\.com/cities`)},

	// --- Analytics / tracking (business-signal, even though we block them)
	{name: "Google Analytics", category: "analytics",
		bodyRE: regexp.MustCompile(`(?i)google-analytics\.com/(analytics|ga)\.js|gtag/js|googletagmanager\.com/gtag`)},
	{name: "Meta Pixel", category: "analytics",
		bodyRE: regexp.MustCompile(`(?i)connect\.facebook\.net/[^"]+/fbevents\.js|fbq\('init'`)},
	{name: "Hotjar", category: "analytics",
		bodyRE: regexp.MustCompile(`(?i)static\.hotjar\.com|hotjar\.com/c/hotjar`)},
	{name: "Matomo", category: "analytics",
		bodyRE: regexp.MustCompile(`(?i)matomo\.js|piwik\.js|/matomo\.php`)},

	// --- Chat / live support
	{name: "Intercom", category: "chat",
		bodyRE: regexp.MustCompile(`(?i)widget\.intercom\.io|Intercom\("boot"`)},
	{name: "Crisp", category: "chat",
		bodyRE: regexp.MustCompile(`(?i)client\.crisp\.chat`)},
	{name: "Zendesk", category: "chat",
		bodyRE: regexp.MustCompile(`(?i)static\.zdassets\.com|zendesk\.com/embeddable`)},
	{name: "Tawk.to", category: "chat",
		bodyRE: regexp.MustCompile(`(?i)embed\.tawk\.to`)},

	// --- Hosting hints
	{name: "Cloudflare", category: "hosting",
		headerRE: regexp.MustCompile(`(?i)server: cloudflare|cf-ray:`)},
	{name: "Vercel", category: "hosting",
		headerRE: regexp.MustCompile(`(?i)server: vercel|x-vercel-`)},
	{name: "Netlify", category: "hosting",
		headerRE: regexp.MustCompile(`(?i)server: netlify|x-nf-request-id`)},
}

// DetectTech returns the (deduped, sorted) list of techs present on a page.
// body should be the HTML; headers is the response header map. Both are
// optional — passing one is enough to get a partial signal.
func DetectTech(body []byte, headers map[string][]string) []Tech {
	// Scan only the prefix of the body for speed; these signals always
	// live in the <head> or the first <script> block.
	scan := body
	if len(scan) > 128*1024 {
		scan = scan[:128*1024]
	}

	var headerBlob string
	for k, vs := range headers {
		for _, v := range vs {
			headerBlob += k + ": " + v + "\n"
		}
	}

	seen := map[string]*Tech{}

	for i := range techSignatures {
		sig := &techSignatures[i]

		matched := false
		version := ""

		if sig.headerRE != nil && sig.headerRE.MatchString(headerBlob) {
			matched = true
		}

		if sig.bodyRE != nil {
			if m := sig.bodyRE.FindSubmatch(scan); len(m) > 0 {
				matched = true
				if sig.versionSubmatch > 0 && sig.versionSubmatch < len(m) {
					version = strings.TrimSpace(string(m[sig.versionSubmatch]))
				}
			}
		}

		if !matched {
			continue
		}

		if existing, ok := seen[sig.name]; ok {
			// Promote version if we had none before.
			if existing.Version == "" && version != "" {
				existing.Version = version
			}
			continue
		}

		seen[sig.name] = &Tech{Name: sig.name, Category: sig.category, Version: version}
	}

	out := make([]Tech, 0, len(seen))
	for _, t := range seen {
		out = append(out, *t)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})

	return out
}
