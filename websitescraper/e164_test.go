package websitescraper

import "testing"

func TestNormaliseE164(t *testing.T) {
	cases := []struct {
		raw     string
		country string
		want    string
	}{
		// Already E.164.
		{"+33 1 42 00 00 00", "FR", "+33142000000"},
		{"+1 (415) 555 1212", "US", "+14155551212"},
		{"+44 20 7946 0018", "GB", "+442079460018"},

		// National form with trunk prefix.
		{"01.42.00.00.01", "FR", "+33142000001"},
		{"0 30 1234 5678", "DE", "+493012345678"},
		{"020 7946 0018", "UK", "+442079460018"},

		// Italy has no trunk prefix (trunk=""): Italian numbers retain
		// their leading 0 in international form, so we emit +39 + the
		// full national digits including the 0.
		{"06 1234 5678", "IT", "+390612345678"},

		// Unknown country leaves prefix alone.
		{"5551234567", "XX", "+5551234567"},

		// Too short and too long return verbatim.
		{"123", "FR", "123"},
		{"1234567890123456789", "FR", "1234567890123456789"},

		// Empty is empty.
		{"", "FR", ""},

		// Explicit + with clean digits.
		{"+34 911 234 567", "", "+34911234567"},
	}

	for _, c := range cases {
		got := NormaliseE164(c.raw, c.country)
		if got != c.want {
			t.Errorf("NormaliseE164(%q, %q) = %q, want %q", c.raw, c.country, got, c.want)
		}
	}
}

func TestNormaliseE164SliceDedupes(t *testing.T) {
	in := []string{
		"+33 1 42 00 00 00",
		"01 42 00 00 00", // same number, national form
		"+33 1 42 00 00 01",
	}

	got := NormaliseE164Slice(in, "FR")

	if len(got) != 2 {
		t.Fatalf("expected 2 unique phones, got %d: %v", len(got), got)
	}
}
