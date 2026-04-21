package gmaps

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/gosom/scrapemate"
)

// WriteGeoJSON serialises entries as a GeoJSON FeatureCollection. Each entry
// becomes a Point feature at (lon, lat); its non-spatial attributes land on
// the feature's properties object. Tools that consume GeoJSON directly —
// QGIS, Mapbox, Leaflet, kepler.gl, DuckDB spatial extension — will render
// the file without any transformation.
//
// Entries without coordinates are skipped (a Feature needs a geometry).
func WriteGeoJSON(w io.Writer, entries []*Entry) error {
	type geometry struct {
		Type        string    `json:"type"`
		Coordinates []float64 `json:"coordinates"`
	}

	type feature struct {
		Type       string         `json:"type"`
		Geometry   geometry       `json:"geometry"`
		Properties map[string]any `json:"properties"`
	}

	type featureCollection struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}

	fc := featureCollection{Type: "FeatureCollection", Features: make([]feature, 0, len(entries))}

	for _, e := range entries {
		if e == nil || (e.Latitude == 0 && e.Longtitude == 0) {
			continue
		}

		props := map[string]any{
			"id":          e.ID,
			"title":       e.Title,
			"category":    e.Category,
			"categories":  e.Categories,
			"address":     e.Address,
			"website":     e.WebSite,
			"phone":       e.Phone,
			"rating":      e.ReviewRating,
			"reviews":     e.ReviewCount,
			"plus_code":   e.PlusCode,
			"place_id":    e.PlaceID,
			"link":        e.Link,
			"status":      e.Status,
			"timezone":    e.Timezone,
			"price_range": e.PriceRange,
			"description": e.Description,
			"emails":      e.Emails,
		}

		if wc := e.WebsiteContact; wc != nil {
			props["website_phones"] = wc.Phones
			props["socials"] = wc.SocialLinks
			props["tech_stack"] = wc.TechStack
			if wc.DomainAgeYears > 0 {
				props["domain_age_years"] = wc.DomainAgeYears
			}
		}

		fc.Features = append(fc.Features, feature{
			Type: "Feature",
			Geometry: geometry{
				Type:        "Point",
				Coordinates: []float64{e.Longtitude, e.Latitude}, // GeoJSON = [lon, lat]
			},
			Properties: props,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(fc)
}

// GeoJSONWriter implements scrapemate.ResultWriter by accumulating entries
// and flushing them as a FeatureCollection when the input channel closes.
// GeoJSON cannot be appended line-by-line without custom JSON surgery, so
// we buffer like XLSXWriter does.
type GeoJSONWriter struct {
	w io.WriteCloser

	mu      sync.Mutex
	entries []*Entry
}

// NewGeoJSONWriter returns a writer that will flush to w when the result
// channel closes. w is closed after the flush.
func NewGeoJSONWriter(w io.WriteCloser) *GeoJSONWriter { return &GeoJSONWriter{w: w} }

// Run consumes *Entry results until the channel closes, then writes a
// single FeatureCollection and closes the output.
func (g *GeoJSONWriter) Run(_ context.Context, in <-chan scrapemate.Result) error {
	for result := range in {
		g.collect(result.Data)
	}

	g.mu.Lock()
	entries := g.entries
	g.entries = nil
	g.mu.Unlock()

	defer func() { _ = g.w.Close() }()

	return WriteGeoJSON(g.w, entries)
}

func (g *GeoJSONWriter) collect(data any) {
	if data == nil {
		return
	}

	if e, ok := data.(*Entry); ok {
		g.mu.Lock()
		g.entries = append(g.entries, e)
		g.mu.Unlock()

		return
	}

	if entries, ok := data.([]*Entry); ok {
		g.mu.Lock()
		g.entries = append(g.entries, entries...)
		g.mu.Unlock()
	}
}
