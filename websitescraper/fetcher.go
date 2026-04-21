// Package websitescraper fetches and analyses the public-facing pages of a
// business website in order to enrich a gmaps.Entry with contact data (emails,
// phones, social links, structured data) that Google Maps does not surface.
//
// Anti-bot:
//   - Default path uses azuretls-client, which spoofs the TLS fingerprint and
//     HTTP/2 frame order of real Chrome so a lot of Cloudflare "IUAM" and
//     "managed challenge" pages just pass. Cookies are preserved across the
//     whole crawl of one site.
//   - If FLARESOLVERR_URL is set, any response that still looks like a
//     Cloudflare interstitial ("Just a moment...", HTTP 403+challenge) is
//     retried through the FlareSolverr proxy (github.com/FlareSolverr/FlareSolverr)
//     which solves JS/captcha challenges in a real Chromium under the hood.
package websitescraper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	tls_client "github.com/Noooste/azuretls-client"
)

// ErrCloudflare is returned when the response is still a Cloudflare challenge
// page after every available bypass has been tried.
var ErrCloudflare = errors.New("websitescraper: cloudflare challenge not solved")

// Response is the normalised shape the analyser consumes, abstracting over
// the underlying transport (azuretls, FlareSolverr, or a direct playwright
// fallback).
type Response struct {
	URL        string
	FinalURL   string
	StatusCode int
	Body       []byte
	Headers    map[string][]string
	FetchedAt  time.Time
}

// Fetcher retrieves a URL with stealth defaults, falling back to FlareSolverr
// when available and a Cloudflare interstitial is detected.
type Fetcher struct {
	mu       sync.Mutex
	session  *tls_client.Session
	timeout  time.Duration
	flare    *flareSolverrClient // nil when FLARESOLVERR_URL is unset
	maxBytes int64
}

// NewFetcher creates a Fetcher. Browser fingerprint defaults to Chrome; a
// fresh cookie jar survives the lifetime of the Fetcher so cookies set on
// the root page are re-sent on /contact, /about, etc.
func NewFetcher() *Fetcher {
	s := tls_client.NewSession()
	s.Browser = tls_client.Chrome

	f := &Fetcher{
		session:  s,
		timeout:  20 * time.Second,
		maxBytes: 2 * 1024 * 1024, // 2 MB is plenty for HTML pages; anything bigger is usually a CDN error page or spam
	}

	if url := strings.TrimRight(os.Getenv("FLARESOLVERR_URL"), "/"); url != "" {
		f.flare = &flareSolverrClient{baseURL: url, timeout: 60 * time.Second}
	}

	return f
}

// FlareSolverrEnabled reports whether the fallback is configured. Useful for
// logging and test skips.
func (f *Fetcher) FlareSolverrEnabled() bool { return f != nil && f.flare != nil }

// Close releases the underlying TLS session.
func (f *Fetcher) Close() {
	if f == nil || f.session == nil {
		return
	}

	f.session.Close()
}

// SetTimeout overrides the per-request HTTP timeout (default 20 s).
func (f *Fetcher) SetTimeout(d time.Duration) {
	if d > 0 {
		f.timeout = d
	}
}

// Get fetches a URL, honouring ctx, and transparently retries through
// FlareSolverr if a Cloudflare challenge is still present on the response.
func (f *Fetcher) Get(ctx context.Context, rawURL string) (*Response, error) {
	if f == nil {
		return nil, errors.New("websitescraper: nil fetcher")
	}

	if _, err := url.Parse(rawURL); err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", rawURL, err)
	}

	// azuretls session is NOT safe for concurrent use from our callers: the
	// crawler serialises sub-page fetches per site, but be defensive here so
	// a future concurrent caller does not corrupt the cookie jar.
	f.mu.Lock()
	resp, err := f.doAzure(ctx, rawURL)
	f.mu.Unlock()

	if err != nil && f.flare == nil {
		return nil, err
	}

	if err == nil && !isCloudflareChallenge(resp) {
		return resp, nil
	}

	// Fallback path: ask FlareSolverr to solve the challenge for us.
	if f.flare == nil {
		return resp, ErrCloudflare
	}

	flared, ferr := f.flare.Get(ctx, rawURL)
	if ferr != nil {
		if resp != nil {
			return resp, fmt.Errorf("flaresolverr fallback failed: %w (original status %d)", ferr, resp.StatusCode)
		}

		return nil, fmt.Errorf("flaresolverr fallback failed: %w", ferr)
	}

	return flared, nil
}

func (f *Fetcher) doAzure(ctx context.Context, rawURL string) (*Response, error) {
	// Bound the per-request wait by both our timeout and the caller's ctx.
	reqCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	s := f.session

	// Propagate cancellation INTO azuretls so Do aborts instead of blocking
	// us in a detached goroutine. The Fetcher mutex guarantees no concurrent
	// Do() call on the same session, so SetContext is safe here.
	s.SetContext(reqCtx)

	req := &tls_client.Request{
		Method: "GET",
		Url:    rawURL,
		OrderedHeaders: tls_client.OrderedHeaders{
			{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
			{"accept-language", "en-US,en;q=0.9"},
			{"accept-encoding", "gzip, deflate, br"},
			{"upgrade-insecure-requests", "1"},
			{"sec-fetch-site", "none"},
			{"sec-fetch-mode", "navigate"},
			{"sec-fetch-user", "?1"},
			{"sec-fetch-dest", "document"},
		},
	}

	// We still need the goroutine-as-watchdog pattern because s.Do does not
	// always honour ctx fast-enough on TLS handshake failures. But now that
	// we've SetContext'd the session, the goroutine will unwind on its own
	// within the timeout instead of leaking until some later GC event.
	type azResult struct {
		resp *tls_client.Response
		err  error
	}

	done := make(chan azResult, 1)

	go func() {
		r, e := s.Do(req)
		done <- azResult{resp: r, err: e}
	}()

	var resp *tls_client.Response
	var err error

	select {
	case r := <-done:
		resp, err = r.resp, r.err
	case <-reqCtx.Done():
		return nil, reqCtx.Err()
	}

	if err != nil {
		return nil, err
	}

	body := resp.Body
	if int64(len(body)) > f.maxBytes {
		body = body[:f.maxBytes]
	}

	headers := map[string][]string{}
	for _, h := range resp.Header {
		headers[h[0]] = append(headers[h[0]], h[1])
	}

	final := rawURL
	if resp.Url != "" {
		final = resp.Url
	}

	return &Response{
		URL:        rawURL,
		FinalURL:   final,
		StatusCode: resp.StatusCode,
		Body:       body,
		Headers:    headers,
		FetchedAt:  time.Now(),
	}, nil
}

// isCloudflareChallenge detects the common "interstitial" signals Cloudflare
// emits when it wants JS or a captcha solved. We use body markers + headers
// so both the classic 5xx managed-challenge and the newer 403 "Sorry, you
// have been blocked" variants are caught.
func isCloudflareChallenge(r *Response) bool {
	if r == nil {
		return false
	}

	if r.StatusCode == 403 || r.StatusCode == 503 || r.StatusCode == 429 {
		if hasAny(r.Headers, []string{"cf-mitigated", "cf-chl-bypass", "cf-ray"}) {
			return true
		}
	}

	if len(r.Body) == 0 {
		return false
	}

	scan := r.Body
	if len(scan) > 64*1024 {
		scan = scan[:64*1024]
	}

	l := strings.ToLower(string(scan))
	markers := []string{
		"checking your browser",
		"just a moment",
		"cf-challenge",
		"__cf_chl_jschl_tk__",
		"cf_chl_opt",
		"attention required | cloudflare",
		"please verify you are human",
	}

	for _, m := range markers {
		if strings.Contains(l, m) {
			return true
		}
	}

	return false
}

func hasAny(headers map[string][]string, keys []string) bool {
	for k := range headers {
		lk := strings.ToLower(k)
		for _, w := range keys {
			if lk == w {
				return true
			}
		}
	}

	return false
}

// drainAndClose is a tiny helper we keep around in case future code paths
// hold on to io.ReadCloser bodies.
func drainAndClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}

	_, _ = io.Copy(io.Discard, rc)

	_ = rc.Close()
}
