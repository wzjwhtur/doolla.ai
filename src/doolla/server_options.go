package doolla

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// ServerOptions permits explicitly trusted browser origins to embed the UI.
type ServerOptions struct {
	Replay         ReplayOptions
	AllowedOrigins []string
}

func allowedOrigin(origins []string, origin string) bool {
	return slices.Contains(origins, origin)
}

type trustedOriginKey struct{}

func cors(next http.Handler, origins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if r.Method != http.MethodGet && !originMatchesHost(r) && !allowedOrigin(origins, origin) {
				http.Error(w, "Origin is not allowed", http.StatusForbidden)
				return
			}
			if allowedOrigin(origins, origin) {
				r = r.WithContext(context.WithValue(r.Context(), trustedOriginKey{}, origin))
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func originMatchesHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(r.Host, u.Host)
}
