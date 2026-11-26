// Package webui serves the repository's compiled web component from the Go binary.
package webui

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"strings"

	"doolla.ai/sdk/internal/i18n"
)

//go:generate npm ci --no-audit --no-fund
//go:generate npm run build
//go:embed doolla-components.js.gz
var bundle []byte

//go:embed api.html
var apiDocs []byte

//go:embed openapi.json
var apiSchema []byte

var docsTemplate = template.Must(template.New("docs").Funcs(template.FuncMap{"t": i18n.Format}).Parse(string(apiDocs)))

var schemaTranslations = map[string]string{
	"Doolla Chat API":   "server.docs.title",
	"Server health":     "server.docs.health",
	"Server is running": "server.docs.healthy",
	"Send a message and receive all chat components in one response": "server.docs.poll",
	"Completed turn":         "server.docs.poll_success",
	"Invalid chat request":   "server.docs.invalid",
	"User resolution failed": "server.docs.unauthorized",
	"Request ID conflicts with a prior turn or its response is unavailable":                                                           "server.docs.conflict",
	"Request cache or active request limit reached":                                                                                   "server.docs.full",
	"Stream chat components as server-sent events":                                                                                    "server.docs.sse",
	"Each data event contains a ChatChunk. The final event contains [DONE].":                                                          "server.docs.sse_success",
	"Upgrade to a WebSocket and exchange chat messages":                                                                               "server.docs.ws",
	"Send one ChatRequest per text frame. Each response contains ChatChunk frames followed by a completion frame, or an error frame.": "server.docs.ws_use",
	"Switching protocols": "server.docs.ws_success",
	"Optional language tag. Supported locales are en and zh-CN.":                                                              "server.docs.locale",
	"The last chat chunk is turn_outcome. Its data.status is succeeded, partial, or failed, and data.code may give a reason.": "server.docs.turn_outcome",
}

func ServeDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = docsTemplate.Execute(w, i18n.Resolve(i18n.Requested(r.Context()), "", ""))
}

func ServeSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	locale := i18n.Requested(r.Context())
	if locale != i18n.Chinese {
		_, _ = w.Write(apiSchema)
		return
	}
	var schema map[string]any
	if err := json.Unmarshal(apiSchema, &schema); err != nil {
		http.Error(w, i18n.Format(locale, "server.error.request_incomplete"), http.StatusInternalServerError)
		return
	}
	localizeSchema(schema, locale)
	_ = json.NewEncoder(w).Encode(schema)
}

func localizeSchema(node map[string]any, locale string) {
	for field, value := range node {
		switch entry := value.(type) {
		case map[string]any:
			localizeSchema(entry, locale)
		case string:
			if key := schemaTranslations[entry]; key != "" && (field == "title" || field == "summary" || field == "description") {
				node[field] = i18n.Format(locale, key)
			}
		}
	}
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
