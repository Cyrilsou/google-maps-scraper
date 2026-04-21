package web

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Service struct {
	repo       JobRepository
	dataFolder string
}

func NewService(repo JobRepository, dataFolder string) *Service {
	return &Service{
		repo:       repo,
		dataFolder: dataFolder,
	}
}

func (s *Service) Create(ctx context.Context, job *Job) error {
	return s.repo.Create(ctx, job)
}

func (s *Service) All(ctx context.Context) ([]Job, error) {
	jobs, err := s.repo.Select(ctx, SelectParams{})
	if err != nil {
		return nil, err
	}

	for i := range jobs {
		if jobs[i].Status == StatusOK || jobs[i].Status == StatusWorking {
			jobs[i].ResultCount = s.ResultCount(jobs[i].ID)
		}
	}

	return jobs, nil
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return job, err
	}

	if job.Status == StatusOK || job.Status == StatusWorking {
		job.ResultCount = s.ResultCount(job.ID)
	}

	return job, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid file name")
	}

	// Remove every possible artefact file (CSV, XLSX, JSONL, GeoJSON).
	// Missing files are fine.
	for _, ext := range []string{".csv", ".xlsx", ".jsonl", ".geojson"} {
		datapath := filepath.Join(s.dataFolder, id+ext)
		if _, err := os.Stat(datapath); err == nil {
			if err := os.Remove(datapath); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, job *Job) error {
	return s.repo.Update(ctx, job)
}

func (s *Service) SelectPending(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{Status: StatusPending, Limit: 1})
}

func (s *Service) GetCSV(_ context.Context, id string) (string, error) {
	return s.ResultFile(id, FormatCSV)
}

// ResultFile returns the path to the result artefact for a job, picking the
// CSV or XLSX variant based on format. If format is empty, both are checked
// (XLSX first) so callers can stay format-agnostic.
func (s *Service) ResultFile(id, format string) (string, error) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid file name")
	}

	try := []string{format}
	if format == "" {
		// Preference order when auto-detecting: richest export first.
		try = []string{FormatXLSX, FormatGeoJSON, FormatJSONL, FormatCSV}
	}

	for _, f := range try {
		if f == "" {
			continue
		}

		path := filepath.Join(s.dataFolder, id+"."+f)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("result file not found for job %s", id)
}

// ResultCount returns how many rows are in the CSV or JSONL export for id.
// XLSX is intentionally not probed — reading it requires decompressing the
// whole workbook and is too slow for a table-hydration call. Returns 0 when
// no file exists (jobs still pending, or XLSX-only exports).
func (s *Service) ResultCount(id string) int {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return 0
	}

	// Prefer JSONL (one record per line, zero parsing) when available.
	if path := filepath.Join(s.dataFolder, id+"."+FormatJSONL); fileExists(path) {
		return countLines(path)
	}

	if path := filepath.Join(s.dataFolder, id+"."+FormatCSV); fileExists(path) {
		// Subtract 1 for the header row, but never report negative.
		n := countLines(path) - 1
		if n < 0 {
			return 0
		}

		return n
	}

	return 0
}

// PreviewResult is a small subset of Entry fields exposed to the web UI for
// the in-browser results preview. Keeping this local avoids a heavy gmaps
// import chain from the web package's public API.
type PreviewResult struct {
	Title    string  `json:"title"`
	Category string  `json:"category"`
	Address  string  `json:"address"`
	Website  string  `json:"website"`
	Phone    string  `json:"phone"`
	Rating   float64 `json:"rating"`
	Reviews  int     `json:"reviews"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Link     string  `json:"link"`
}

// Preview returns up to limit rows from the result artefact. CSV is parsed
// by column name (order-agnostic); JSONL is decoded directly. Returns a
// nil slice when no file is available.
func (s *Service) Preview(id string, limit int) ([]PreviewResult, error) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return nil, fmt.Errorf("invalid file name")
	}

	if limit <= 0 {
		limit = 50
	}

	const hardCap = 500
	if limit > hardCap {
		limit = hardCap
	}

	// Prefer JSONL (structured, cheapest to read) then CSV.
	if path := filepath.Join(s.dataFolder, id+"."+FormatJSONL); fileExists(path) {
		return previewFromJSONL(path, limit)
	}

	if path := filepath.Join(s.dataFolder, id+"."+FormatCSV); fileExists(path) {
		return previewFromCSV(path, limit)
	}

	return nil, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

func countLines(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	count := 0

	for {
		n, err := f.Read(buf)
		for i := 0; i < n; i++ {
			if buf[i] == '\n' {
				count++
			}
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			return count
		}
	}

	return count
}

func previewFromJSONL(path string, limit int) ([]PreviewResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make([]PreviewResult, 0, limit)

	scanner := bufio.NewScanner(f)
	// Some review payloads can be large; bump the buffer so long lines
	// do not silently truncate the preview.
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)

	for scanner.Scan() && len(out) < limit {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Decode into a minimal struct that accepts both the exact web
		// preview fields and the richer gmaps.Entry shape.
		var e struct {
			Title       string  `json:"title"`
			Category    string  `json:"category"`
			Address     string  `json:"address"`
			WebSite     string  `json:"web_site"`
			Phone       string  `json:"phone"`
			Rating      float64 `json:"review_rating"`
			ReviewCount int     `json:"review_count"`
			Latitude    float64 `json:"latitude"`
			Longtitude  float64 `json:"longtitude"`
			Link        string  `json:"link"`
		}

		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}

		out = append(out, PreviewResult{
			Title:    e.Title,
			Category: e.Category,
			Address:  e.Address,
			Website:  e.WebSite,
			Phone:    e.Phone,
			Rating:   e.Rating,
			Reviews:  e.ReviewCount,
			Lat:      e.Latitude,
			Lon:      e.Longtitude,
			Link:     e.Link,
		})
	}

	return out, scanner.Err()
}

func previewFromCSV(path string, limit int) ([]PreviewResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Skip UTF-8 BOM if present (we emit one on CSV output).
	r := bufio.NewReader(f)
	if b, _ := r.Peek(3); len(b) == 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = r.Discard(3)
	}

	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1

	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}

	idx := make(map[string]int, len(headers))
	for i, h := range headers {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	get := func(row []string, name string) string {
		if i, ok := idx[name]; ok && i < len(row) {
			return row[i]
		}

		return ""
	}

	out := make([]PreviewResult, 0, limit)

	for len(out) < limit {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			// A single malformed row should not abort the whole preview.
			continue
		}

		var rating float64
		fmt.Sscanf(get(row, "review_rating"), "%f", &rating)

		var lat, lon float64
		fmt.Sscanf(get(row, "latitude"), "%f", &lat)
		fmt.Sscanf(get(row, "longitude"), "%f", &lon)

		var reviews int
		fmt.Sscanf(get(row, "review_count"), "%d", &reviews)

		out = append(out, PreviewResult{
			Title:    get(row, "title"),
			Category: get(row, "category"),
			Address:  get(row, "address"),
			Website:  get(row, "website"),
			Phone:    get(row, "phone"),
			Rating:   rating,
			Reviews:  reviews,
			Lat:      lat,
			Lon:      lon,
			Link:     get(row, "link"),
		})
	}

	return out, nil
}
