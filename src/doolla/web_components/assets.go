// Package webui serves the repository's compiled web component from the Go binary.
package webui

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"net/http"
	"strings"
)

//go:generate npm ci --no-audit --no-fund
//go:generate npm run build
//go:embed doolla-components.js.gz
var bundle []byte

//go:embed api.html
var apiDocs []byte

//go:embed openapi.json
var apiSchema []byte

func ServeDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(apiDocs)
}

func ServeSchema(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(apiSchema)
}

func Serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")
	for _, entry := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(entry) == "gzip" {
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(bundle)
			return
		}
	}
	reader, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		http.Error(w, "invalid embedded frontend", 500)
		return
	}
	defer reader.Close()
	_, _ = io.Copy(w, reader)
}
