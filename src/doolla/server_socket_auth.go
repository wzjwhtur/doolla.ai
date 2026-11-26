package doolla

import (
	"net/http"
	"time"
)

// Resolvers normally write only cookies. After a WebSocket upgrades there is
// no HTTP response to write to; retain issued cookies for subsequent frames.
type socketResolverResponse struct{ header http.Header }

func (r socketResolverResponse) Header() http.Header         { return r.header }
func (r socketResolverResponse) Write(p []byte) (int, error) { return len(p), nil }
func (r socketResolverResponse) WriteHeader(int)             {}

func withResolverCookies(req *http.Request, header http.Header) *http.Request {
	issued := (&http.Response{Header: header}).Cookies()
	if len(issued) == 0 {
		return req
	}
	overrides := map[string]bool{}
	for _, c := range issued {
		overrides[c.Name] = true
	}
	next := req.Clone(req.Context())
	next.Header.Del("Cookie")
	for _, c := range req.Cookies() {
		if !overrides[c.Name] {
			next.AddCookie(c)
		}
	}
	now := time.Now()
	for _, c := range issued {
		if c.MaxAge >= 0 && (c.Expires.IsZero() || c.Expires.After(now)) {
			next.AddCookie(c)
		}
	}
	return next
}
