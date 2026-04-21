package websitescraper

import (
	"context"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// GuessEmails returns candidate contact addresses of the form
// <pattern>@<domain> built from a full person name. It filters out any
// candidate whose domain does not have an MX/A record.
//
// This is how most small-business emails are actually structured:
// "contact@", "info@", "{firstname}@", "{firstname}.{lastname}@" — we try a
// dozen high-confidence patterns and run them through MX validation so we
// never emit a provably-undeliverable candidate.
//
// Use alongside (not instead of) Analyse: when Analyse finds no email and
// the Organization block carries a contact name, GuessEmails is the next
// best lead.
func GuessEmails(ctx context.Context, fullName, domain string) []string {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return nil
	}

	first, last := splitName(fullName)

	// Generic catch-alls are nearly always worth trying.
	candidates := []string{
		"contact@" + domain,
		"info@" + domain,
		"hello@" + domain,
		"bonjour@" + domain,
		"kontakt@" + domain,
	}

	if first != "" {
		candidates = append(candidates,
			first+"@"+domain,
			first[:1]+"@"+domain,
		)
	}

	if first != "" && last != "" {
		candidates = append(candidates,
			first+"."+last+"@"+domain,
			first+last+"@"+domain,
			first[:1]+last+"@"+domain,
			first+"."+last[:1]+"@"+domain,
			last+"."+first+"@"+domain,
			last+"@"+domain,
		)
	}

	// Dedup + keep order.
	seen := map[string]bool{}
	unique := make([]string, 0, len(candidates))

	for _, c := range candidates {
		if _, ok := seen[c]; ok {
			continue
		}

		seen[c] = true
		unique = append(unique, c)
	}

	// MX-filter: drop every candidate whose domain does not look like it
	// can receive mail.
	valid := ValidateEmails(ctx, unique)

	// Cap to a small set — emitting 15 guesses per lead would pollute the
	// export.
	const maxGuesses = 5
	if len(valid) > maxGuesses {
		valid = valid[:maxGuesses]
	}

	return valid
}

// splitName returns (first, last) normalised to lowercase ASCII. Diacritics
// are stripped so "françois" and "Dupré" map cleanly to the typical email
// alphabet. Multi-word first or last names collapse to the first token —
// "Marie-Claire" becomes "marie" because hyphens rarely survive in real
// email locals.
func splitName(full string) (string, string) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "", ""
	}

	parts := strings.Fields(full)
	if len(parts) == 0 {
		return "", ""
	}

	first := normaliseNamePart(parts[0])

	last := ""
	if len(parts) > 1 {
		last = normaliseNamePart(parts[len(parts)-1])
	}

	return first, last
}

func normaliseNamePart(s string) string {
	// NFD-decompose + drop combining marks to strip diacritics.
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = s
	}

	folded = strings.ToLower(folded)

	// Keep only letters; drop hyphens, apostrophes, spaces, numbers.
	var b strings.Builder

	for _, r := range folded {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}

	return b.String()
}
