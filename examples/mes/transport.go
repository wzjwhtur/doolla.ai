//go:build doolla_presto

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Only the two supported order counts can reach Trino. Short table names use
// the fixed mes/dbo session from the DSN constructed in main.
const ident = `(?:[\pL_][\pL\pN_]*|"[\pL\pN_ ]{1,64}")`
const alias = `(?:\s+(?:as\s+)?` + ident + `)?`
const count = `count\s*\(\s*(?:\*|1|distinct\s+(?:` + ident + `\s*\.\s*)?(?:moid|"moid"))\s*\)` + alias
const table = `(?:(?:(?:mes|"mes")\s*\.\s*)?(?:dbo|"dbo")\s*\.\s*)?(?:pro_mo|"pro_mo")`

var totals = regexp.MustCompile(`(?i)^\s*select\s+` + count + `(?:\s*,\s*` + count + `)?\s+from\s+` + table + alias + `\s*;?\s*$`)
var ping = regexp.MustCompile(`(?i)^\s*select\s+1\s*;?\s*$`)
var resultPage = regexp.MustCompile(`^/v1/statement/(?:queued|executing)/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[0-9]+$`)

type mesTransport struct {
	origin *url.URL
	next   http.RoundTripper
	logger *log.Logger
}

func (m mesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	if u.Scheme != m.origin.Scheme || u.Host != m.origin.Host || u.User != nil || u.Fragment != "" {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, errors.New("Trino requests and result pages must use the configured coordinator")
	}
	r := req.Clone(req.Context())
	switch r.Method {
	case http.MethodPost:
		if r.Body == nil {
			return nil, errors.New("missing Trino statement")
		}
		defer req.Body.Close()
		if u.EscapedPath() != "/v1/statement" || u.RawQuery != "" {
			return nil, errors.New("unexpected Trino statement path")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
		if err != nil {
			return nil, err
		}
		if len(body) > 8192 || (!totals.Match(body) && !ping.Match(body)) {
			return nil, errors.New("MES demo supports COUNT(*) and COUNT(DISTINCT moid) on mes.dbo.pro_mo")
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	case http.MethodGet, http.MethodDelete:
		// The Presto driver obtains these paths from Trino's nextUri, including
		// the queued and executing stages. Cancellation uses DELETE on nextUri.
		if !resultPage.MatchString(u.EscapedPath()) {
			return nil, errors.New("unexpected Trino result page path")
		}
	default:
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, errors.New("unexpected Trino request method")
	}
	for key, values := range req.Header {
		if suffix, ok := strings.CutPrefix(http.CanonicalHeaderKey(key), "X-Presto-"); ok {
			r.Header["X-Trino-"+suffix] = values
			r.Header.Del(key)
		}
	}
	resp, err := m.next.RoundTrip(r)
	if err != nil || r.Method != http.MethodPost || resp.StatusCode/100 != 2 {
		return resp, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("unexpectedly large initial Trino response")
	}
	var info struct{ ID string }
	if err := json.Unmarshal(data, &info); err != nil || info.ID == "" {
		return nil, fmt.Errorf("Trino returned no query ID (HTTP %d)", resp.StatusCode)
	}
	m.logger.Printf("Trino query_id=%q", info.ID)
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}
