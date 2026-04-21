package web

import "testing"

func TestJobDataResolvedFormat(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", FormatCSV},
		{"csv", FormatCSV},
		{"CSV", FormatCSV}, // unknown values fall back to CSV
		{FormatXLSX, FormatXLSX},
		{FormatJSONL, FormatJSONL},
		{"json", FormatJSONL}, // legacy alias
	}

	for _, c := range cases {
		d := &JobData{Format: c.in}
		if got := d.ResolvedFormat(); got != c.want {
			t.Errorf("ResolvedFormat(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
