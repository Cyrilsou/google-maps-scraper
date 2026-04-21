package lambdaaws

type lInput struct {
	JobID            string   `json:"job_id"`
	Part             int      `json:"part"`
	BucketName       string   `json:"bucket_name"`
	Keywords         []string `json:"keywords"`
	Depth            int      `json:"depth"`
	Concurrency      int      `json:"concurrency"`
	Language         string   `json:"language"`
	FunctionName     string   `json:"function_name"`
	DisablePageReuse bool     `json:"disable_page_reuse"`
	ExtraReviews     bool     `json:"extra_reviews"`
	// FastMode enables the HTTP-only search path (no browser) when true.
	// Geo + Zoom + Radius become mandatory in this mode.
	FastMode bool `json:"fast_mode"`
	// Geo is the center coordinate "lat,lon" used in FastMode or when
	// combined with Zoom to anchor the browser search URL.
	Geo string `json:"geo,omitempty"`
	// Zoom is the Google Maps zoom level (1-21). Required with FastMode.
	Zoom int `json:"zoom,omitempty"`
	// Radius is the search radius in meters. 0 falls back to 10 km.
	Radius float64 `json:"radius,omitempty"`
	// Email triggers website email extraction after a place is scraped.
	Email bool `json:"email,omitempty"`
}
