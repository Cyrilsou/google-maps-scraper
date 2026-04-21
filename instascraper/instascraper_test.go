package instascraper

import (
	"testing"
)

func TestExtractHandle(t *testing.T) {
	cases := map[string]string{
		// Bare forms.
		"john":                "john",
		"john.doe":            "john.doe",
		"@john":               "john",
		"  @John  ":           "john",
		"john_doe123":         "john_doe123",

		// URLs.
		"https://www.instagram.com/john/":         "john",
		"https://www.instagram.com/john":          "john",
		"instagram.com/john":                      "john",
		"http://instagram.com/john/reel/abc":      "john",

		// Reserved reserved paths are rejected.
		"https://www.instagram.com/explore":  "",
		"https://www.instagram.com/p/XYZ/":   "",
		"https://www.instagram.com/reels":    "",
		"https://www.instagram.com/stories":  "",

		// Garbage.
		"":          "",
		"   ":       "",
		"http://":   "",
		"john!":     "", // ! not allowed
	}

	for in, want := range cases {
		if got := extractHandle(in); got != want {
			t.Errorf("extractHandle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyMetaTags(t *testing.T) {
	html := `<!doctype html>
<html>
<head>
<meta property="og:title" content="Boulangerie Dupont (@dupont_bakery) • Instagram photos and videos"/>
<meta property="og:image" content="https://scontent.cdninstagram.com/avatar.jpg"/>
<meta property="og:description" content="12.3K Followers, 842 Following, 1,245 Posts - See Instagram photos and videos from Boulangerie Dupont (@dupont_bakery) on Instagram: &quot;Pain, pâtisserie, et café — Paris 11e. Commandes : dupont@example.fr&quot;"/>
</head>
<body></body>
</html>`

	p := &Profile{Handle: "dupont_bakery"}
	applyMetaTags([]byte(html), p)

	if p.FullName != "Boulangerie Dupont" {
		t.Errorf("FullName = %q", p.FullName)
	}

	if p.ProfilePicture == "" {
		t.Error("ProfilePicture not populated from og:image")
	}

	if p.FollowerCount != 12300 {
		t.Errorf("FollowerCount = %d (want 12300 from 12.3K)", p.FollowerCount)
	}

	if p.FollowingCount != 842 {
		t.Errorf("FollowingCount = %d", p.FollowingCount)
	}

	if p.PostCount != 1245 {
		t.Errorf("PostCount = %d (want 1245 from '1,245 Posts')", p.PostCount)
	}
}

func TestParseAbbrev(t *testing.T) {
	cases := map[string]int{
		"1,234":   1234,
		"1.2K":    1200,
		"3.4M":    3400000,
		"42B":     42000000000,
		"15k":     15000,
		"  999 ":  999,
		"":        0,
		"notnum":  0,
	}

	for in, want := range cases {
		if got := parseAbbrev(in); got != want {
			t.Errorf("parseAbbrev(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestApplyEmbeddedJSONFindsUserObject(t *testing.T) {
	// A trimmed-down version of the real SSR payload shape Instagram
	// emits. The user object is buried after some unrelated JSON to
	// make sure findMatchingBrace correctly isolates it.
	html := `<script>{"unrelated":{"foo":"bar"},"user":{"biography":"Pain et café","full_name":"Boulangerie Dupont","external_url":"https://dupont.fr","is_verified":true,"is_business_account":true,"business_category_name":"Bakery","profile_pic_url_hd":"https://cdn/hd.jpg","edge_followed_by":{"count":12345},"edge_follow":{"count":300},"edge_owner_to_timeline_media":{"count":870}}}</script>`

	p := &Profile{Handle: "dupont_bakery"}
	applyEmbeddedJSON([]byte(html), p)

	if p.Bio != "Pain et café" {
		t.Errorf("Bio = %q", p.Bio)
	}

	if p.FullName != "Boulangerie Dupont" {
		t.Errorf("FullName = %q", p.FullName)
	}

	if p.ExternalURL != "https://dupont.fr" {
		t.Errorf("ExternalURL = %q", p.ExternalURL)
	}

	if !p.IsVerified {
		t.Error("IsVerified not extracted")
	}

	if !p.IsBusiness {
		t.Error("IsBusiness not extracted")
	}

	if p.Category != "Bakery" {
		t.Errorf("Category = %q", p.Category)
	}

	if p.FollowerCount != 12345 {
		t.Errorf("FollowerCount = %d", p.FollowerCount)
	}

	if p.PostCount != 870 {
		t.Errorf("PostCount = %d", p.PostCount)
	}
}

func TestFindMatchingBraceHandlesStrings(t *testing.T) {
	// The scan must ignore braces that appear inside strings, including
	// escaped quotes. Without that, the isolation of a user object gives
	// up one character too early.
	s := `{"a":"this { is not a brace","b":"escaped \"quote\" then }","c":{"nested":"ok"}}`

	end := findMatchingBrace(s, 0)
	if end != len(s)-1 {
		t.Errorf("findMatchingBrace returned %d, want %d", end, len(s)-1)
	}
}
