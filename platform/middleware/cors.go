package middleware

import (
	"net/http"
	"slices"
)

// CORS lets browsers on allowedOrigins call the service. "*" allows any
// origin. Credentials are not supported; use github.com/rs/cors if you
// need them or the rest of the spec.
//
// Place it outside Recover and Timeout so their 500 and 504 responses
// carry CORS headers too; otherwise the browser hides them from the page.
func CORS(allowedOrigins []string) Middleware {
	allowAll := slices.Contains(allowedOrigins, "*")
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// The response depends on Origin, so caches must key on it.
			h.Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			if origin == "" || !(allowAll || allowed[origin]) {
				next.ServeHTTP(w, r)
				return
			}

			h.Set("Access-Control-Allow-Origin", origin)

			// A preflight is an OPTIONS request that names the method it
			// wants to use. Other OPTIONS requests go to the routes.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// Let browser code read the request ID for bug reports.
			h.Set("Access-Control-Expose-Headers", RequestIDHeader)
			next.ServeHTTP(w, r)
		})
	}
}
