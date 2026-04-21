package websitescraper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// flareSolverrClient talks to a FlareSolverr instance
// (github.com/FlareSolverr/FlareSolverr). Configure with the FLARESOLVERR_URL
// env var — e.g. http://127.0.0.1:8191.
//
// We only use the sessionless "request.get" command: starting a session per
// host would require lifecycle management (create/destroy) and the HTML we
// want is almost always on the landing/contact pages where a fresh session
// is fine.
type flareSolverrClient struct {
	baseURL string
	timeout time.Duration
}

type flareRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type flareResponseSolution struct {
	URL      string              `json:"url"`
	Status   int                 `json:"status"`
	Response string              `json:"response"`
	Headers  map[string]string   `json:"headers"`
	Cookies  []map[string]any    `json:"cookies"`
	Meta     map[string]any      `json:"meta"`
	Other    map[string]any      `json:"-"`
	RawJSON  json.RawMessage     `json:"-"`
	Iter     map[string][]string `json:"-"`
}

type flareResponse struct {
	Status   string                `json:"status"`
	Message  string                `json:"message"`
	Solution flareResponseSolution `json:"solution"`
}

// Get solves a Cloudflare challenge and returns the underlying HTML.
func (c *flareSolverrClient) Get(ctx context.Context, targetURL string) (*Response, error) {
	if c == nil || c.baseURL == "" {
		return nil, errors.New("flaresolverr: not configured")
	}

	reqBody := flareRequest{
		Cmd:        "request.get",
		URL:        targetURL,
		MaxTimeout: int(c.timeout / time.Millisecond),
	}

	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// Cap the outer HTTP wait a bit higher than FlareSolverr's own
	// maxTimeout so we do not time out BEFORE the solver finishes.
	httpCtx, cancel := context.WithTimeout(ctx, c.timeout+10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodPost, c.baseURL+"/v1", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flaresolverr returned HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}

	var parsed flareResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("flaresolverr: invalid JSON: %w", err)
	}

	if parsed.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", parsed.Message)
	}

	headers := map[string][]string{}
	for k, v := range parsed.Solution.Headers {
		headers[k] = []string{v}
	}

	return &Response{
		URL:        targetURL,
		FinalURL:   parsed.Solution.URL,
		StatusCode: parsed.Solution.Status,
		Body:       []byte(parsed.Solution.Response),
		Headers:    headers,
		FetchedAt:  time.Now(),
	}, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}

	return string(b[:n]) + "..."
}
