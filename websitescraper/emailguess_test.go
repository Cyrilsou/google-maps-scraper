package websitescraper

import (
	"testing"
)

func TestSplitName(t *testing.T) {
	cases := []struct {
		in         string
		wantFirst  string
		wantLast   string
	}{
		{"Marie Dupont", "marie", "dupont"},
		{"François Durand", "francois", "durand"},
		{"Anne-Claire Dubois", "anneclaire", "dubois"},
		{"Jean", "jean", ""},
		{"  ", "", ""},
		{"", "", ""},
		{"Müller Schmidt", "muller", "schmidt"},
	}

	for _, c := range cases {
		f, l := splitName(c.in)
		if f != c.wantFirst || l != c.wantLast {
			t.Errorf("splitName(%q) = (%q,%q), want (%q,%q)", c.in, f, l, c.wantFirst, c.wantLast)
		}
	}
}

func TestNormaliseNamePart(t *testing.T) {
	cases := map[string]string{
		"José":         "jose",
		"Müller":       "muller",
		"O'Brien":      "obrien",
		"Jean-Luc":     "jeanluc",
		"MARIE":        "marie",
		"123":          "",
		"Anne Marie":   "annemarie",
	}

	for in, want := range cases {
		if got := normaliseNamePart(in); got != want {
			t.Errorf("normaliseNamePart(%q) = %q, want %q", in, got, want)
		}
	}
}
