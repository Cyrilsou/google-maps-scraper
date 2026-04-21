// Package gmaps — Excel exporter.
//
// WriteXLSX serialises a stream of *Entry values into a multi-sheet workbook
// that is significantly more readable in Excel/LibreOffice than the default
// CSV output: nested structures (opening hours, reviews, images…) land on
// dedicated sheets instead of being JSON-stringified inside a single cell.
package gmaps

import (
	"io"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	sheetMain    = "Places"
	sheetHours   = "Opening hours"
	sheetReviews = "Reviews"
	sheetImages  = "Images"
)

// mainHeaders lists the flat columns on the main sheet. Nested fields that
// would produce unreadable JSON blobs in a cell are moved to dedicated sheets.
var mainHeaders = []string{
	"input_id",
	"link",
	"title",
	"category",
	"categories",
	"address",
	"street",
	"city",
	"postal_code",
	"state",
	"country",
	"website",
	"phone",
	"plus_code",
	"review_count",
	"review_rating",
	"latitude",
	"longitude",
	"cid",
	"status",
	"description",
	"reviews_link",
	"thumbnail",
	"timezone",
	"price_range",
	"data_id",
	"place_id",
	"menu_link",
	"owner_name",
	"owner_link",
	"emails",
	"image_count",
	"review_extracted_count",
}

// WriteXLSX streams entries into an XLSX workbook and writes the result to w.
//
// The workbook contains:
//   - "Places": one row per entry, with the most useful flat fields.
//   - "Opening hours": one row per (place, day) pair.
//   - "Reviews": one row per review (both inline and RPC-extracted).
//   - "Images": one row per image URL.
func WriteXLSX(w io.Writer, entries []*Entry) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	if err := writePlacesSheet(f, entries); err != nil {
		return err
	}

	if err := writeHoursSheet(f, entries); err != nil {
		return err
	}

	if err := writeReviewsSheet(f, entries); err != nil {
		return err
	}

	if err := writeImagesSheet(f, entries); err != nil {
		return err
	}

	// Place "Places" first in the tab bar.
	if idx, err := f.GetSheetIndex(sheetMain); err == nil {
		f.SetActiveSheet(idx)
	}

	_ = f.DeleteSheet("Sheet1")

	return f.Write(w)
}

func writePlacesSheet(f *excelize.File, entries []*Entry) error {
	if _, err := f.NewSheet(sheetMain); err != nil {
		return err
	}

	sw, err := f.NewStreamWriter(sheetMain)
	if err != nil {
		return err
	}

	headerRow := make([]any, len(mainHeaders))
	for i, h := range mainHeaders {
		headerRow[i] = h
	}

	if err := sw.SetRow("A1", headerRow); err != nil {
		return err
	}

	// Freeze the header row so it stays visible while scrolling.
	if err := sw.SetPanes(&excelize.Panes{Freeze: true, Split: false, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}

	for i, e := range entries {
		if e == nil {
			continue
		}

		cell, err := excelize.CoordinatesToCellName(1, i+2)
		if err != nil {
			return err
		}

		row := []any{
			e.ID,
			e.Link,
			e.Title,
			e.Category,
			strings.Join(e.Categories, "; "),
			e.Address,
			e.CompleteAddress.Street,
			e.CompleteAddress.City,
			e.CompleteAddress.PostalCode,
			e.CompleteAddress.State,
			e.CompleteAddress.Country,
			e.WebSite,
			e.Phone,
			e.PlusCode,
			e.ReviewCount,
			e.ReviewRating,
			e.Latitude,
			e.Longtitude,
			e.Cid,
			e.Status,
			e.Description,
			e.ReviewsLink,
			e.Thumbnail,
			e.Timezone,
			e.PriceRange,
			e.DataID,
			e.PlaceID,
			e.Menu.Link,
			e.Owner.Name,
			e.Owner.Link,
			strings.Join(e.Emails, "; "),
			len(e.Images),
			len(e.UserReviews) + len(e.UserReviewsExtended),
		}

		if err := sw.SetRow(cell, row); err != nil {
			return err
		}
	}

	return sw.Flush()
}

func writeHoursSheet(f *excelize.File, entries []*Entry) error {
	if _, err := f.NewSheet(sheetHours); err != nil {
		return err
	}

	sw, err := f.NewStreamWriter(sheetHours)
	if err != nil {
		return err
	}

	if err := sw.SetRow("A1", []any{"place_id", "title", "day", "hours"}); err != nil {
		return err
	}

	row := 2

	for _, e := range entries {
		if e == nil {
			continue
		}

		// Use a stable day ordering instead of Go's random map iteration.
		for _, day := range weekdayOrder(e.OpenHours) {
			slots := e.OpenHours[day]
			cell, err := excelize.CoordinatesToCellName(1, row)
			if err != nil {
				return err
			}

			if err := sw.SetRow(cell, []any{e.PlaceID, e.Title, day, strings.Join(slots, ", ")}); err != nil {
				return err
			}

			row++
		}
	}

	return sw.Flush()
}

func writeReviewsSheet(f *excelize.File, entries []*Entry) error {
	if _, err := f.NewSheet(sheetReviews); err != nil {
		return err
	}

	sw, err := f.NewStreamWriter(sheetReviews)
	if err != nil {
		return err
	}

	if err := sw.SetRow("A1", []any{"place_id", "title", "source", "reviewer", "rating", "when", "text", "images"}); err != nil {
		return err
	}

	row := 2

	writeReview := func(e *Entry, source string, rv Review) error {
		cell, err := excelize.CoordinatesToCellName(1, row)
		if err != nil {
			return err
		}

		if err := sw.SetRow(cell, []any{
			e.PlaceID,
			e.Title,
			source,
			rv.Name,
			rv.Rating,
			rv.When,
			rv.Description,
			strings.Join(rv.Images, "; "),
		}); err != nil {
			return err
		}

		row++

		return nil
	}

	for _, e := range entries {
		if e == nil {
			continue
		}

		for _, rv := range e.UserReviews {
			if err := writeReview(e, "inline", rv); err != nil {
				return err
			}
		}

		for _, rv := range e.UserReviewsExtended {
			if err := writeReview(e, "extra", rv); err != nil {
				return err
			}
		}
	}

	return sw.Flush()
}

func writeImagesSheet(f *excelize.File, entries []*Entry) error {
	if _, err := f.NewSheet(sheetImages); err != nil {
		return err
	}

	sw, err := f.NewStreamWriter(sheetImages)
	if err != nil {
		return err
	}

	if err := sw.SetRow("A1", []any{"place_id", "title", "caption", "image_url"}); err != nil {
		return err
	}

	row := 2

	for _, e := range entries {
		if e == nil {
			continue
		}

		for _, img := range e.Images {
			cell, err := excelize.CoordinatesToCellName(1, row)
			if err != nil {
				return err
			}

			if err := sw.SetRow(cell, []any{e.PlaceID, e.Title, img.Title, img.Image}); err != nil {
				return err
			}

			row++
		}
	}

	return sw.Flush()
}

// weekdayOrder returns the weekdays present in hours, ordered Mon→Sun. Keys
// that are not standard weekday names fall at the end, alphabetically.
func weekdayOrder(hours map[string][]string) []string {
	if len(hours) == 0 {
		return nil
	}

	canonical := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

	out := make([]string, 0, len(hours))
	seen := make(map[string]struct{}, len(hours))

	for _, d := range canonical {
		if _, ok := hours[d]; ok {
			out = append(out, d)
			seen[d] = struct{}{}
		}
	}

	// Add any non-standard keys (languages other than English, etc.) in a
	// deterministic order.
	for k := range hours {
		if _, ok := seen[k]; ok {
			continue
		}

		out = append(out, k)
	}

	return out
}

// SheetSuffix is the canonical file extension for the Excel export.
const SheetSuffix = ".xlsx"
