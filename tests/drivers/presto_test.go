//go:build doolla_presto

package drivers_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/prestodb/presto-go-client/presto"
	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
	_ "doolla.ai/sdk/integrations/database/drivers"
)

func TestPrestoReadsCompleteResultSet(t *testing.T) {
	for _, test := range []struct {
		name         string
		failLastPage bool
		empty        bool
	}{
		{name: "success"},
		{name: "empty", empty: true},
		{name: "failed page", failLastPage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if user, pass, ok := r.BasicAuth(); !ok || user != "analyst" || pass != "fixture" {
					t.Error("missing Presto HTTPS authentication")
				}
				if r.Method == http.MethodPost && r.Header.Get("X-Presto-User") != "analyst" {
					t.Error("missing Presto user header")
				}
				switch r.Method + " " + r.URL.Path {
				case "POST /v1/statement":
					query, err := io.ReadAll(r.Body)
					if err != nil || string(query) != "SELECT answer FROM numbers" {
						t.Errorf("query = %q, err = %v", query, err)
					}
					fmt.Fprintf(w, `{"id":"query","nextUri":"https://%s/first"}`, r.Host)
				case "GET /first":
					if test.empty {
						fmt.Fprint(w, `{"id":"query","columns":[{"name":"answer","type":"bigint","typeSignature":{"rawType":"bigint"}}],"data":[]}`)
						return
					}
					fmt.Fprintf(w, `{"id":"query","nextUri":"https://%s/last","columns":[{"name":"answer","type":"bigint","typeSignature":{"rawType":"bigint"}}],"data":[[41]]}`, r.Host)
				case "GET /last":
					if test.failLastPage {
						fmt.Fprint(w, `{"id":"query","error":{"message":"Table is unavailable","errorName":"TABLE_NOT_FOUND","errorType":"USER_ERROR"}}`)
						return
					}
					fmt.Fprint(w, `{"id":"query","data":[[42]]}`)
				case "DELETE /last":
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			u.User = url.UserPassword("analyst", "fixture")
			const clientName = "doolla-presto-protocol-test"
			if err := presto.RegisterCustomClient(clientName, server.Client()); err != nil {
				t.Fatal(err)
			}
			defer presto.DeregisterCustomClient(clientName)
			u.RawQuery = url.Values{"custom_client": {clientName}}.Encode()
			db, err := database.Open("presto", u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			result, err := (doolla.DatabaseRunner{DB: db}).RunSQL(t.Context(), doolla.User{}, "SELECT answer FROM numbers;\n")
			if test.failLastPage {
				var queryErr *presto.ErrQueryFailed
				if !errors.As(err, &queryErr) {
					t.Fatalf("expected a Presto query error after the first page, got %v", err)
				}
				return
			}
			want := doolla.SQLResult{
				Columns: []string{"answer"},
				Rows:    []map[string]any{{"answer": int64(41)}, {"answer": int64(42)}},
			}
			if test.empty {
				want.Rows = []map[string]any{}
			}
			if err != nil || !reflect.DeepEqual(result, want) {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
		})
	}
}
