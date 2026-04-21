package websitescraper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// CaptchaSolver is a pluggable interface for third-party captcha solvers.
// Today only 2captcha is implemented, but Anti-Captcha / CapMonster speak a
// nearly identical in=/res= protocol so adding them is a few lines.
type CaptchaSolver interface {
	// SolveReCaptchaV2 resolves a reCAPTCHA v2 challenge. sitekey is the
	// data-sitekey attribute of the widget, pageURL is the page it appears
	// on. Returns the g-recaptcha-response token to POST back.
	SolveReCaptchaV2(ctx context.Context, sitekey, pageURL string) (string, error)
	// SolveTurnstile resolves a Cloudflare Turnstile challenge, same idea.
	SolveTurnstile(ctx context.Context, sitekey, pageURL string) (string, error)
}

// DefaultSolver is the solver used by the crawler when TWOCAPTCHA_API_KEY is
// set. Exported so tests can stub it.
var DefaultSolver CaptchaSolver

func init() {
	if key := os.Getenv("TWOCAPTCHA_API_KEY"); key != "" {
		DefaultSolver = NewTwoCaptcha(key)
	}
}

// TwoCaptcha is a thin, dependency-free client for the 2captcha.com "in.php"
// / "res.php" API. The same protocol is used verbatim by rucaptcha and by
// older anti-captcha shims, so it works against several providers by just
// pointing APIHost at them.
type TwoCaptcha struct {
	APIKey  string
	APIHost string // default: https://2captcha.com
	Client  *http.Client
	Timeout time.Duration
}

// NewTwoCaptcha builds a client with sensible defaults.
func NewTwoCaptcha(apiKey string) *TwoCaptcha {
	return &TwoCaptcha{
		APIKey:  apiKey,
		APIHost: "https://2captcha.com",
		Client:  &http.Client{Timeout: 30 * time.Second},
		Timeout: 180 * time.Second,
	}
}

// SolveReCaptchaV2 submits the challenge and polls for the result. Errors
// wrap the 2captcha error code verbatim so callers can build provider-
// specific retry logic (ERROR_ZERO_BALANCE is not the same as
// ERROR_CAPTCHA_UNSOLVABLE).
func (t *TwoCaptcha) SolveReCaptchaV2(ctx context.Context, sitekey, pageURL string) (string, error) {
	params := url.Values{}
	params.Set("key", t.APIKey)
	params.Set("method", "userrecaptcha")
	params.Set("googlekey", sitekey)
	params.Set("pageurl", pageURL)
	params.Set("json", "1")

	return t.submitAndPoll(ctx, params)
}

// SolveTurnstile is the Cloudflare Turnstile equivalent.
func (t *TwoCaptcha) SolveTurnstile(ctx context.Context, sitekey, pageURL string) (string, error) {
	params := url.Values{}
	params.Set("key", t.APIKey)
	params.Set("method", "turnstile")
	params.Set("sitekey", sitekey)
	params.Set("pageurl", pageURL)
	params.Set("json", "1")

	return t.submitAndPoll(ctx, params)
}

type twoCaptchaResp struct {
	Status  int    `json:"status"`
	Request string `json:"request"`
}

func (t *TwoCaptcha) submitAndPoll(ctx context.Context, params url.Values) (string, error) {
	if t.APIKey == "" {
		return "", errors.New("2captcha: empty api key")
	}

	// 1. in.php — submit the task, get a taskID.
	taskID, err := t.postJSON(ctx, t.APIHost+"/in.php", params)
	if err != nil {
		return "", fmt.Errorf("2captcha submit: %w", err)
	}

	// 2. res.php — poll until "OK|<token>". The provider docs recommend
	//    first-poll at 15 s then every 5 s.
	deadline := time.Now().Add(t.Timeout)
	first := true

	for time.Now().Before(deadline) {
		wait := 5 * time.Second
		if first {
			wait = 15 * time.Second
			first = false
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}

		res := url.Values{}
		res.Set("key", t.APIKey)
		res.Set("action", "get")
		res.Set("id", taskID)
		res.Set("json", "1")

		token, err := t.postJSON(ctx, t.APIHost+"/res.php", res)
		if err != nil {
			if err.Error() == "2captcha: CAPCHA_NOT_READY" {
				continue
			}

			return "", fmt.Errorf("2captcha poll: %w", err)
		}

		return token, nil
	}

	return "", errors.New("2captcha: timed out waiting for solution")
}

// postJSON sends a form-encoded POST and parses the 2captcha JSON envelope.
// "OK" → returns request; anything else → error carrying the provider code.
func (t *TwoCaptcha) postJSON(ctx context.Context, endpoint string, params url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(params.Encode()))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return "", err
	}

	var parsed twoCaptchaResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("2captcha: invalid json: %s", truncate(body, 200))
	}

	if parsed.Status != 1 {
		// The 2captcha response field carries the error code on Status=0.
		return "", fmt.Errorf("2captcha: %s", parsed.Request)
	}

	return parsed.Request, nil
}
