// Package webhook streams scraper results to an operator-configured HTTP
// endpoint while the job is still running, so integrations (CRMs, spreadsheets,
// data warehouses) can react immediately instead of polling for completion.
//
// Activation: set RESULT_WEBHOOK_URL. An optional RESULT_WEBHOOK_SECRET turns
// on HMAC-SHA256 signing (X-Webhook-Signature header, "sha256=<hex>"); receivers
// compare that against their own HMAC of the raw body.
//
// Delivery is best-effort and fire-and-forget from the caller's perspective:
// posts run on a bounded worker pool so a slow receiver never blocks the
// scraping hot path. Failed posts are retried with exponential backoff; if the
// retries do not succeed, the result is dropped and a counter increments.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics tracks webhook delivery outcomes for observability.
type Metrics struct {
	Sent      atomic.Int64
	Failed    atomic.Int64
	Dropped   atomic.Int64
	QueueHigh atomic.Int64 // high-water mark of the queue depth
}

// DefaultMetrics is exposed so /metrics handlers can report it.
var DefaultMetrics Metrics

// Client posts JSON payloads to an webhook URL.
//
// Closing is synchronised via closeMu: Enqueue takes an RLock for the
// duration of the channel send, Close takes the write Lock before calling
// close(). This is the standard Go pattern for "multiple producers +
// one closer", and it avoids the panic-on-closed-channel that would
// otherwise bite us under shutdown.
type Client struct {
	url        string
	secret     string
	http       *http.Client
	queue      chan payload
	wg         sync.WaitGroup
	maxRetries int
	closeMu    sync.RWMutex
	closed     atomic.Bool
}

type payload struct {
	Topic string `json:"topic"`
	Body  any    `json:"body"`
	Time  int64  `json:"ts"`
}

// NewFromEnv returns a Client configured from RESULT_WEBHOOK_URL /
// RESULT_WEBHOOK_SECRET, or nil if no URL is set. The returned Client
// spawns its own worker goroutines; call Close to drain the queue.
func NewFromEnv() *Client {
	url := os.Getenv("RESULT_WEBHOOK_URL")
	if url == "" {
		return nil
	}

	workers := 4
	if v := os.Getenv("RESULT_WEBHOOK_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 32 {
			workers = n
		}
	}

	queueSize := 1024
	if v := os.Getenv("RESULT_WEBHOOK_QUEUE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100000 {
			queueSize = n
		}
	}

	c := &Client{
		url:        url,
		secret:     os.Getenv("RESULT_WEBHOOK_SECRET"),
		http:       &http.Client{Timeout: 15 * time.Second},
		queue:      make(chan payload, queueSize),
		maxRetries: 3,
	}

	for i := 0; i < workers; i++ {
		c.wg.Add(1)
		go c.worker()
	}

	return c
}

// Enqueue drops a topic/body pair into the send queue. Non-blocking: if the
// queue is full the payload is dropped (DefaultMetrics.Dropped bumped) rather
// than back-pressuring the scraper. Backpressure on the hot path is worse
// than losing one webhook delivery in an outage.
//
// Send-on-closed-channel is prevented by taking closeMu.RLock for the
// entire select — Close acquires the write lock before calling close(),
// so we are guaranteed a consistent view of c.closed and the channel.
func (c *Client) Enqueue(topic string, body any) {
	if c == nil {
		return
	}

	c.closeMu.RLock()
	defer c.closeMu.RUnlock()

	if c.closed.Load() {
		return
	}

	p := payload{Topic: topic, Body: body, Time: time.Now().Unix()}

	select {
	case c.queue <- p:
		depth := int64(len(c.queue))
		for {
			cur := DefaultMetrics.QueueHigh.Load()
			if depth <= cur || DefaultMetrics.QueueHigh.CompareAndSwap(cur, depth) {
				break
			}
		}
	default:
		DefaultMetrics.Dropped.Add(1)
	}
}

// Close signals the workers to stop, drains the queue, and waits for
// in-flight deliveries to complete.
func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}

	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	// Wait for in-flight Enqueues to release their RLock before we close
	// the channel. Without this, a producer that observed closed=false
	// would race with our close() and panic on the send.
	c.closeMu.Lock()
	close(c.queue)
	c.closeMu.Unlock()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) worker() {
	defer c.wg.Done()

	for p := range c.queue {
		if err := c.deliver(p); err != nil {
			DefaultMetrics.Failed.Add(1)
		} else {
			DefaultMetrics.Sent.Add(1)
		}
	}
}

func (c *Client) deliver(p payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}

	var lastErr error

	backoff := 500 * time.Millisecond
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}

		err := c.post(body)
		if err == nil {
			return nil
		}

		lastErr = err

		// Permanent 4xx errors (except 408/429) should not be retried —
		// they won't get better on retry and we just waste the budget.
		var perr permanentError
		if errors.As(err, &perr) {
			return err
		}
	}

	return lastErr
}

type permanentError struct{ status int }

func (e permanentError) Error() string { return fmt.Sprintf("permanent webhook error: HTTP %d", e.status) }

func (c *Client) post(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gmaps-scraper-webhook/1")

	if c.secret != "" {
		mac := hmac.New(sha256.New, []byte(c.secret))
		_, _ = mac.Write(body)
		req.Header.Set("X-Webhook-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Drain the body so HTTP keep-alive can reuse the connection. Cheap.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	// 408 Request Timeout and 429 Too Many Requests are transient; every
	// other 4xx is permanent.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
		resp.StatusCode != http.StatusRequestTimeout &&
		resp.StatusCode != http.StatusTooManyRequests {
		return permanentError{status: resp.StatusCode}
	}

	return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
}
