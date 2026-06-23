package cliconfig

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli-config" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("cli-config must be unauthenticated, got Authorization header")
		}
		w.Write([]byte(`{"enterprise":true,"productName":"Acme AI Hub","oauthClientId":"cid","dashboardBaseUrl":"https://blocks.acme.com"}`))
	}))
	defer srv.Close()

	cfg, err := Fetch(srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !cfg.Enterprise || cfg.ProductName != "Acme AI Hub" || cfg.OAuthClientID != "cid" {
		t.Fatalf("unexpected: %+v", cfg)
	}
	if cfg.DashboardBaseURL != "https://blocks.acme.com" {
		t.Fatalf("dashboardBaseUrl not decoded: %+v", cfg)
	}
}

func TestFetchMissingEndpointIsNotEnterprise(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cfg, err := Fetch(srv.URL)
	if err != nil {
		t.Fatalf("Fetch should be lenient on 404, got %v", err)
	}
	if cfg.Enterprise {
		t.Fatalf("404 must resolve to non-enterprise")
	}
}

func TestFetchEmptyURLIsNotEnterprise(t *testing.T) {
	cfg, err := Fetch("")
	if err != nil {
		t.Fatalf("Fetch(\"\") should be lenient, got %v", err)
	}
	if cfg.Enterprise || cfg.ProductName != "" {
		t.Fatalf("empty URL must resolve to zero value, got %+v", cfg)
	}
}

func TestFetchOmittedOptionalKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stock Network: optional keys omitted entirely (never null).
		w.Write([]byte(`{"enterprise":false,"productName":"Blocks Network"}`))
	}))
	defer srv.Close()
	cfg, err := Fetch(srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if cfg.Enterprise || cfg.ProductName != "Blocks Network" {
		t.Fatalf("unexpected: %+v", cfg)
	}
	if cfg.OAuthClientID != "" || cfg.DashboardBaseURL != "" {
		t.Fatalf("omitted optional keys must decode to empty string, got %+v", cfg)
	}
}

func TestFetchServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := Fetch(srv.URL); err == nil {
		t.Fatalf("a 500 must surface as an error (only 404 is lenient)")
	}
}
