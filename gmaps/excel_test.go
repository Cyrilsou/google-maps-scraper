package gmaps

import "testing"

// TestXLSXHeaderRowCountMatches guards against a class of bug where adding
// a header to mainHeaders without also adding a row value (or vice versa)
// silently corrupts every XLSX export. An integration test that opens a
// real workbook would be slow and flaky; a direct reflection-free count is
// the cheapest check that still catches the whole category.
func TestXLSXHeaderRowCountMatches(t *testing.T) {
	// Build a synthetic entry with a WebsiteContact so every conditional
	// branch in writePlacesSheet runs and contributes a value.
	e := &Entry{
		ID:    "id",
		Title: "T",
		WebsiteContact: &WebsiteContact{
			Phones:      []string{"+1"},
			SocialLinks: map[string]string{"facebook": "https://fb"},
			TechStack:   []TechItem{{Name: "Stripe", Category: "payments"}},
		},
	}

	// placeRowValues mirrors the row build-up in writePlacesSheet. We call
	// the same helper to guarantee the two stay in sync. If this import
	// ever fails to compile because the helper's signature changes, the
	// check has done its job.
	row := placeRowValuesForTest(e)

	if len(row) != len(mainHeaders) {
		t.Fatalf("XLSX header/row count mismatch: %d headers vs %d row values", len(mainHeaders), len(row))
	}
}
