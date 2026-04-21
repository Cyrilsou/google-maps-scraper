package web

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// apiKeyMiddleware enforces an API key on /api/v1/* routes when
// API_KEY (or GMAPS_API_KEY) is set in the environment. When neither is set,
// the middleware is a pass-through so dev setups keep working with zero
// config.
//
// Accepted credential locations, in order:
//   - Authorization: Bearer <key>
//   - X-API-Key: <key>
//   - ?api_key=<key> query parameter (handy for curl; discouraged in prod
//     because the key will land in access logs)
//
// Public-facing routes (the HTML form + its assets) are intentionally left
// unauthenticated so a hosted deployment can still serve the UI to browsers
// while the programmatic API stays locked down.
func apiKeyMiddleware(next http.Handler) http.Handler {
	want := strings.TrimSpace(firstNonEmpty(os.Getenv("API_KEY"), os.Getenv("GMAPS_API_KEY")))
	if want == "" {
		return next
	}

	wantBytes := []byte(want)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		got := extractKey(r)
		if subtle.ConstantTimeCompare([]byte(got), wantBytes) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gmaps-scraper"`)
			renderJSON(w, http.StatusUnauthorized, apiError{
				Code:    http.StatusUnauthorized,
				Message: "API key required (Authorization: Bearer <key> or X-API-Key header)",
			})

			return
		}

		next.ServeHTTP(w, r)
	})
}

// requiresAuth returns true for routes that must be gated behind the API
// key. Anything under /api/v1 except /api/docs (redoc page) is gated;
// /metrics is also gated because the proxy-block counters can reveal
// operational info.
func requiresAuth(path string) bool {
	switch {
	case path == "/metrics":
		return true
	case strings.HasPrefix(path, "/api/v1"):
		return true
	}

	return false
}

func extractKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}

	if v := r.Header.Get("X-API-Key"); v != "" {
		return strings.TrimSpace(v)
	}

	return strings.TrimSpace(r.URL.Query().Get("api_key"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}

	return ""
}
