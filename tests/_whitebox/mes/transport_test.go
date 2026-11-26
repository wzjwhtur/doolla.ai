//go:build doolla_presto

package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOrderCountStatements(t *testing.T) {
	for _, statement := range []string{
		"SELECT COUNT(*) FROM mes.dbo.pro_mo;",
		"SELECT COUNT(DISTINCT moid) AS ids FROM dbo.pro_mo",
		"SELECT COUNT(1) total, COUNT(DISTINCT m.moid) AS unique_ids FROM pro_mo AS m",
		`SELECT count(*) AS "订单数", count(DISTINCT "moid") FROM "mes"."dbo"."pro_mo"`,
	} {
		if !totals.MatchString(statement) {
			t.Errorf("rejected permissible count %q", statement)
		}
	}
	for _, statement := range []string{
		"SELECT moid FROM mes.dbo.pro_mo",
		"SELECT count(*) FROM other.dbo.pro_mo",
		"SELECT count(*) FROM mes.dbo.pro_mo GROUP BY moid",
		"SELECT count(*) FROM mes.dbo.pro_mo JOIN other USING (moid)",
		"SELECT count(*) FROM mes.dbo.pro_mo; SELECT 1",
		"SELECT count(DISTINCT other_column) FROM mes.dbo.pro_mo",
	} {
		if totals.MatchString(statement) || ping.MatchString(statement) {
			t.Errorf("accepted unsupported statement %q", statement)
		}
	}
}

func TestResultPageMustUseConfiguredCoordinator(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Trino-Catalog") != "mes" || r.Header.Get("X-Presto-Catalog") != "" {
			t.Error("Presto headers did not become Trino headers")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()
	origin, err := localEndpoint(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: mesTransport{origin, s.Client().Transport, log.New(io.Discard, "", 0)}}
	const page = "/v1/statement/executing/handed-out-query/slug/1"
	for _, item := range []struct {
		url string
		ok  bool
	}{{s.URL + page, true}, {"http://127.0.0.1:0" + page, false}, {s.URL + "/v1/info", false}} {
		req, err := http.NewRequest(http.MethodGet, item.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Presto-Catalog", "mes")
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if (err == nil) != item.ok {
			t.Errorf("GET %s error=%v", item.url, err)
		}
	}
	for _, raw := range []string{"http://example.invalid:8080", s.URL + "?custom_client=another", s.URL + "/v1/statement"} {
		if _, err := localEndpoint(raw); err == nil {
			t.Errorf("accepted origin %q", raw)
		}
	}
	if err := run(t.Context(), []string{"-trino-url", s.URL, "-dsn", (&url.URL{Scheme: "http", Host: origin.Host}).String()}, io.Discard); err == nil || !strings.Contains(err.Error(), "dsn") {
		t.Fatal("expected explicit DSN override to be rejected")
	}
}
