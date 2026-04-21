package websitescraper

import (
	"strings"
	"testing"
)

const sampleHTML = `
<!doctype html>
<html>
<head>
<title>Boulangerie Dupont</title>
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "LocalBusiness",
  "name": "Boulangerie Dupont",
  "legalName": "SARL Dupont et Fils",
  "vatID": "FR12345678901",
  "foundingDate": "1998-06-15",
  "telephone": "+33 1 42 00 00 00",
  "email": "contact@dupont.fr",
  "address": {
    "@type": "PostalAddress",
    "streetAddress": "12 rue du Pain",
    "postalCode": "75001",
    "addressLocality": "Paris",
    "addressCountry": "FR"
  },
  "sameAs": [
    "https://facebook.com/dupont",
    "https://instagram.com/dupontparis"
  ]
}
</script>
</head>
<body>
<header>
  <a href="mailto:info@dupont.fr?subject=Hello">Email us</a>
  <a href="tel:+33142000001">01 42 00 00 01</a>
</header>

<section>
  <p>Vous pouvez aussi nous joindre au <strong>+33 6 12 34 56 78</strong> ou écrire à rh (at) dupont.fr.</p>
  <p>Our press contact: press [dot] team [at] dupont.fr.</p>
</section>

<footer>
  <a href="https://www.facebook.com/dupontparis">Facebook</a>
  <a href="https://twitter.com/dupontparis">Twitter</a>
  <a href="https://www.linkedin.com/company/dupont-paris">LinkedIn</a>
  <a href="https://x.com/another">X</a>
  <a href="https://youtube.com/@dupontparis">YouTube</a>
  <form action="/contact" method="POST">
    <input type="email" name="email"/>
    <button type="submit">Send</button>
  </form>
</footer>
</body>
</html>
`

func TestAnalyseExtractsEmailsPhonesSocials(t *testing.T) {
	profile := Analyse([]byte(sampleHTML), "https://dupont.fr/", nil)

	assertContains(t, profile.Emails, "info@dupont.fr")
	assertContains(t, profile.Emails, "contact@dupont.fr")
	assertContains(t, profile.Emails, "rh@dupont.fr")
	// The "press [dot] team [at] dupont.fr" obfuscation should resolve.
	assertContains(t, profile.Emails, "press.team@dupont.fr")

	assertContains(t, profile.Phones, "+33142000001")
	// Body-text international number with spaces.
	assertContains(t, profile.Phones, "+33612345678")

	if profile.SocialLinks["facebook"] == "" {
		t.Errorf("expected facebook link, got %v", profile.SocialLinks)
	}

	if profile.SocialLinks["twitter"] == "" {
		t.Errorf("expected twitter link (from twitter.com or x.com), got %v", profile.SocialLinks)
	}

	if profile.SocialLinks["linkedin"] == "" {
		t.Errorf("expected linkedin link, got %v", profile.SocialLinks)
	}

	if profile.SocialLinks["youtube"] == "" {
		t.Errorf("expected youtube link, got %v", profile.SocialLinks)
	}

	if !profile.HasContactForm {
		t.Error("expected contact form to be detected")
	}

	if profile.Organization == nil {
		t.Fatal("expected Organization from JSON-LD")
	}

	org := profile.Organization
	if org.Name != "Boulangerie Dupont" {
		t.Errorf("org.Name = %q", org.Name)
	}

	if org.LegalName != "SARL Dupont et Fils" {
		t.Errorf("org.LegalName = %q", org.LegalName)
	}

	if org.VATID != "FR12345678901" {
		t.Errorf("org.VATID = %q", org.VATID)
	}

	if !strings.Contains(org.Address, "75001") || !strings.Contains(org.Address, "Paris") {
		t.Errorf("org.Address = %q", org.Address)
	}

	if len(org.SameAs) != 2 {
		t.Errorf("org.SameAs len = %d, want 2", len(org.SameAs))
	}
}

func TestAnalyseIgnoresJunkEmails(t *testing.T) {
	junk := `
<html><body>
<a href="mailto:noreply@brand.com">no</a>
<a href="mailto:real@brand.com">real</a>
icon@2x.png
</body></html>`

	profile := Analyse([]byte(junk), "https://brand.com/", nil)

	for _, e := range profile.Emails {
		if e == "noreply@brand.com" {
			t.Error("noreply should be filtered out")
		}
		if e == "icon@2x.png" {
			t.Error("image asset should not be treated as email")
		}
	}

	found := false
	for _, e := range profile.Emails {
		if e == "real@brand.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("real email missing from %v", profile.Emails)
	}
}

func TestIsCloudflareChallenge(t *testing.T) {
	cases := []struct {
		name string
		resp *Response
		want bool
	}{
		{"nil", nil, false},
		{"empty", &Response{}, false},
		{"plain html", &Response{StatusCode: 200, Body: []byte("<html>ok</html>")}, false},
		{"cf header 503", &Response{
			StatusCode: 503,
			Headers:    map[string][]string{"Cf-Ray": {"abc"}},
		}, true},
		{"cf body marker", &Response{
			StatusCode: 200,
			Body:       []byte(`<html>Checking your browser before accessing</html>`),
		}, true},
		{"captcha marker", &Response{
			StatusCode: 403,
			Body:       []byte(`please verify you are human`),
			Headers:    map[string][]string{"CF-Mitigated": {"challenge"}},
		}, true},
	}

	for _, c := range cases {
		got := isCloudflareChallenge(c.resp)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalisePhone(t *testing.T) {
	cases := map[string]string{
		"+33 1 42 00 00 00":    "+33142000000",
		"01.42.00.00.01":       "0142000001",
		"(020) 7946 0018":      "02079460018",
		"  +44 20 7946 0018  ": "+442079460018",
		"":                     "",
	}

	for in, want := range cases {
		if got := normalisePhone(in); got != want {
			t.Errorf("normalisePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func assertContains(t *testing.T, haystack []string, needle string) {
	t.Helper()

	for _, s := range haystack {
		if s == needle {
			return
		}
	}

	t.Errorf("expected %q in %v", needle, haystack)
}
