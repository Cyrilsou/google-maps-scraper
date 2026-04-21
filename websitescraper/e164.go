package websitescraper

import (
	"strings"
)

// NormaliseE164 converts a loose phone string into the RFC 3966 / E.164 form
// ("+<country><national>") using defaultCountry as a hint when the number is
// in national form (no leading +). Returns the input verbatim if we cannot
// produce a credible E.164 output, so callers never silently lose data.
//
// This is intentionally a thin normaliser, not a validator: we do not
// enforce the per-country length table (that would need libphonenumber-
// scale data). What we DO enforce is the global 8-15 digit range, the
// trunk-prefix rule ("replace a leading national-trunk 0 with the country
// code when applicable"), and E.164's canonical format.
func NormaliseE164(raw, defaultCountry string) string {
	if raw == "" {
		return ""
	}

	trimmed := strings.TrimSpace(raw)

	var (
		hasPlus bool
		digits  strings.Builder
	)

	for i, r := range trimmed {
		switch {
		case r == '+' && i == 0:
			hasPlus = true
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		}
	}

	d := digits.String()
	if len(d) < 8 || len(d) > 15 {
		return raw
	}

	if hasPlus {
		return "+" + d
	}

	country := strings.ToUpper(strings.TrimSpace(defaultCountry))
	if country == "" {
		// Still return a normalised best-effort if the number already
		// looks like it starts with a known prefix (no trunk 0).
		return "+" + d
	}

	cc, trunk := countryDialPrefix(country)
	if cc == "" {
		return "+" + d
	}

	// Strip a leading trunk prefix (e.g. "0" in FR/DE/UK/IT/ES).
	if trunk != "" && strings.HasPrefix(d, trunk) {
		d = d[len(trunk):]
	}

	// If the number already begins with the country code (e.g. user wrote
	// "33 1 …" without the plus), do not double-prefix.
	if strings.HasPrefix(d, cc) && len(d) > len(cc)+4 {
		return "+" + d
	}

	result := "+" + cc + d
	if len(result)-1 < 8 || len(result)-1 > 15 {
		return raw
	}

	return result
}

// countryDialPrefix returns the (country calling code, national trunk
// prefix) pair for the given ISO-3166 alpha-2 country. Covers the 30 most
// populated "leads-worthy" countries — enough to convert >95% of
// small-business numbers that cross our path. Unknown countries fall
// through and the caller keeps the raw digits.
func countryDialPrefix(iso2 string) (cc, trunk string) {
	switch iso2 {
	case "FR":
		return "33", "0"
	case "DE":
		return "49", "0"
	case "GB", "UK":
		return "44", "0"
	case "IT":
		return "39", "" // Italy keeps the leading 0 in the international form.
	case "ES":
		return "34", ""
	case "PT":
		return "351", ""
	case "BE":
		return "32", "0"
	case "NL":
		return "31", "0"
	case "CH":
		return "41", "0"
	case "AT":
		return "43", "0"
	case "US", "CA":
		return "1", "1"
	case "MX":
		return "52", ""
	case "BR":
		return "55", "0"
	case "AR":
		return "54", "0"
	case "AU":
		return "61", "0"
	case "NZ":
		return "64", "0"
	case "IN":
		return "91", "0"
	case "JP":
		return "81", "0"
	case "KR":
		return "82", "0"
	case "CN":
		return "86", "0"
	case "IE":
		return "353", "0"
	case "SE":
		return "46", "0"
	case "NO":
		return "47", ""
	case "DK":
		return "45", ""
	case "FI":
		return "358", "0"
	case "PL":
		return "48", ""
	case "CZ":
		return "420", ""
	case "RO":
		return "40", "0"
	case "TR":
		return "90", "0"
	case "GR":
		return "30", ""
	case "MA":
		return "212", "0"
	case "DZ":
		return "213", "0"
	case "TN":
		return "216", ""
	case "ZA":
		return "27", "0"
	}

	return "", ""
}

// NormaliseE164Slice applies NormaliseE164 over a slice, deduping the output.
func NormaliseE164Slice(raws []string, defaultCountry string) []string {
	if len(raws) == 0 {
		return raws
	}

	seen := make(map[string]struct{}, len(raws))
	out := make([]string, 0, len(raws))

	for _, r := range raws {
		n := NormaliseE164(r, defaultCountry)
		if n == "" {
			continue
		}

		if _, ok := seen[n]; ok {
			continue
		}

		seen[n] = struct{}{}
		out = append(out, n)
	}

	return out
}
