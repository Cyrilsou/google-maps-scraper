package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientEnqueuesAndDelivers(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("RESULT_WEBHOOK_URL", srv.URL)

	c := NewFromEnv()
	if c == nil {
		t.Fatal("NewFromEnv returned nil despite RESULT_WEBHOOK_URL being set")
	}

	for i := 0; i < 5; i++ {
		c.Enqueue("result", map[string]any{"i": i})
	}

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Wait briefly — Close already flushes synchronously, but the test
	// server accepts may lag.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && received.Load() < 5 {
		time.Sleep(20 * time.Millisecond)
	}

	if received.Load() != 5 {
		t.Fatalf("expected 5 deliveries, got %d", received.Load())
	}
}

func TestClientSignsPayloadsWithSecret(t *testing.T) {
	const secret = "shhhh"

	var got string
	var bodySeen []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Webhook-Signature")
		bodySeen, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("RESULT_WEBHOOK_URL", srv.URL)
	t.Setenv("RESULT_WEBHOOK_SECRET", secret)

	c := NewFromEnv()
	c.Enqueue("t", "hello")
	_ = c.Close(context.Background())

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(bodySeen)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Errorf("signature mismatch: got %q want %q", got, want)
	}
}

func TestClientPermanent4xxStopsRetrying(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	t.Setenv("RESULT_WEBHOOK_URL", srv.URL)

	c := NewFromEnv()
	c.Enqueue("t", 1)
	_ = c.Close(context.Background())

	// Expect a single hit — 400 is permanent, so no retries.
	if hits.Load() != 1 {
		t.Errorf("expected 1 hit (no retry on 400), got %d", hits.Load())
	}
}

func TestClientNilSafe(t *testing.T) {
	var c *Client
	c.Enqueue("x", 1)
	if err := c.Close(context.Background()); err != nil {
		t.Errorf("Close on nil: %v", err)
	}
}
