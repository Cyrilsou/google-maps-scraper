package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newSimpleHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestAPIKeyMiddlewareDisabledWhenUnset(t *testing.T) {
	t.Setenv("API_KEY", "")
	t.Setenv("GMAPS_API_KEY", "")

	h := apiKeyMiddleware(newSimpleHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("without API key configured, request should pass: got %d", rec.Code)
	}
}

func TestAPIKeyMiddlewareRejectsUnauth(t *testing.T) {
	t.Setenv("API_KEY", "secret-key")

	h := apiKeyMiddleware(newSimpleHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAPIKeyMiddlewareAcceptsBearerAndHeader(t *testing.T) {
	t.Setenv("API_KEY", "secret-key")

	h := apiKeyMiddleware(newSimpleHandler())

	cases := map[string]func(r *http.Request){
		"bearer":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret-key") },
		"x-api-key": func(r *http.Request) { r.Header.Set("X-API-Key", "secret-key") },
		"query":     func(r *http.Request) {},
	}

	for name, mutate := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
		if name == "query" {
			req = httptest.NewRequest(http.MethodGet, "/api/v1/jobs?api_key=secret-key", nil)
		} else {
			mutate(req)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", name, rec.Code)
		}
	}
}

func TestAPIKeyMiddlewareSkipsPublicRoutes(t *testing.T) {
	t.Setenv("API_KEY", "secret-key")

	h := apiKeyMiddleware(newSimpleHandler())

	publicPaths := []string{"/", "/static/css/main.css", "/download", "/jobs"}
	for _, p := range publicPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("public path %s should not require auth, got %d", p, rec.Code)
		}
	}
}

func TestAPIKeyMiddlewareGatesMetrics(t *testing.T) {
	t.Setenv("API_KEY", "secret-key")

	h := apiKeyMiddleware(newSimpleHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/metrics must be gated, got %d", rec.Code)
	}
}
