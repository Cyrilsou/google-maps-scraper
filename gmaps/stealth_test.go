package gmaps

import (
	"strings"
	"testing"
)

func TestJitterMSRespectsBounds(t *testing.T) {
	const base = 1000
	const iters = 5000
	const spread = base * 3 / 10

	min := base - spread
	max := base + spread

	for i := 0; i < iters; i++ {
		got := jitterMS(base)
		if got < min || got > max {
			t.Fatalf("jitterMS(%d) returned %d, want in [%d,%d]", base, got, min, max)
		}
	}
}

func TestJitterMSZeroIsZero(t *testing.T) {
	if got := jitterMS(0); got != 0 {
		t.Fatalf("jitterMS(0) = %d, want 0", got)
	}
}

func TestJitterMSSmallBaseIsStable(t *testing.T) {
	// For base=1, spread is 0 so the function short-circuits to base.
	if got := jitterMS(1); got != 1 {
		t.Fatalf("jitterMS(1) = %d, want 1", got)
	}
}

func TestIsBlockedResponseByURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://www.google.com/maps/place/Foo", false},
		{"https://www.google.com/sorry/index?continue=https://maps.google.com", true},
		{"https://www.google.com/httpservice/retry/enablejs", true},
		{"", false},
	}

	for _, c := range cases {
		if got := isBlockedResponse(c.url, nil); got != c.want {
			t.Errorf("isBlockedResponse(%q, nil) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestIsBlockedResponseByBody(t *testing.T) {
	body := []byte(`<html><body>Our systems have detected unusual traffic from your computer.<div class="g-recaptcha"></div></body></html>`)
	if !isBlockedResponse("https://example.com", body) {
		t.Fatal("expected block detection on CAPTCHA body")
	}

	// Large body with marker near the front - verify we scan the prefix.
	prefix := strings.Repeat("x", 8*1024)
	body2 := []byte(prefix + "<script>grecaptcha.render()</script>" + "g-recaptcha")
	if !isBlockedResponse("https://example.com", body2) {
		t.Fatal("expected block detection with marker in prefix")
	}

	// Benign body.
	if isBlockedResponse("https://example.com", []byte("<html>hello</html>")) {
		t.Fatal("benign page flagged as blocked")
	}
}
