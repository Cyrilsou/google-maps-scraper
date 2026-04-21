package websitescraper

import (
	"testing"
)

func TestDetectTech(t *testing.T) {
	body := []byte(`
<html>
<head>
<meta name="generator" content="WordPress 6.3.2"/>
<link rel="stylesheet" href="/wp-content/themes/foo/style.css">
<script src="https://js.stripe.com/v3/"></script>
<script src="https://assets.calendly.com/assets/external/widget.js"></script>
<script src="https://www.googletagmanager.com/gtag/js?id=G-ABC"></script>
<script src="https://widget.intercom.io/widget/abc123"></script>
</head>
<body>
<form class="woocommerce-form"></form>
</body>
</html>
`)
	headers := map[string][]string{
		"Server": {"cloudflare"},
		"Cf-Ray": {"abc-CDG"},
	}

	techs := DetectTech(body, headers)

	want := map[string]string{
		"WordPress":        "cms",
		"WooCommerce":      "ecommerce",
		"Stripe":           "payments",
		"Calendly":         "booking",
		"Google Analytics": "analytics",
		"Intercom":         "chat",
		"Cloudflare":       "hosting",
	}

	got := map[string]string{}
	for _, t := range techs {
		got[t.Name] = t.Category
	}

	for name, cat := range want {
		if got[name] != cat {
			t.Errorf("missing or wrong category for %s: got %q want %q", name, got[name], cat)
		}
	}

	// Check version extraction for WordPress.
	for _, tech := range techs {
		if tech.Name == "WordPress" && tech.Version == "" {
			t.Errorf("WordPress version not extracted")
		}
	}
}

func TestDetectTechNoHit(t *testing.T) {
	body := []byte(`<html><body><p>plain page</p></body></html>`)
	techs := DetectTech(body, nil)
	if len(techs) != 0 {
		t.Errorf("expected no tech detected, got %v", techs)
	}
}
