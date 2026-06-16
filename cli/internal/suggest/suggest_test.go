package suggest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

const sampleBody = `{
  "agents": [
    {"agentName": "translator", "displayName": "Acme Translator", "iconUrl": null},
    {"agentName": "transcribe_audio", "displayName": "Whisper Transcriber", "iconUrl": null},
    {"agentName": "", "displayName": "skip me", "iconUrl": null}
  ],
  "tags": [],
  "providers": [],
  "categories": []
}`

func TestAgents_ParsesAndSetsQuery(t *testing.T) {
	var gotQuery, gotField, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/registry/suggest" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query().Get("q")
		gotField = r.URL.Query().Get("field")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleBody))
	}))
	defer srv.Close()

	client := blocksapi.NewClient(srv.URL, "test-api-key")
	got, err := Agents(context.Background(), client, "trans")
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if gotQuery != "trans" {
		t.Errorf("q = %q, want trans", gotQuery)
	}
	if gotField != "agentname" {
		t.Errorf("field = %q, want agentname", gotField)
	}
	if gotLimit != "10" {
		t.Errorf("limit = %q, want 10", gotLimit)
	}
	// The empty-agentName entry is dropped.
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want 2", len(got))
	}
	if got[0].AgentName != "translator" || got[0].DisplayName != "Acme Translator" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].AgentName != "transcribe_audio" {
		t.Errorf("got[1].AgentName = %q", got[1].AgentName)
	}
}

func TestAgents_ShortQueryNoNetworkCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := blocksapi.NewClient(srv.URL, "k")
	got, err := Agents(context.Background(), client, "t")
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for short query", got)
	}
	if called {
		t.Error("server was called for a sub-minimum query")
	}
}

func TestAgents_NilClient(t *testing.T) {
	got, err := Agents(context.Background(), nil, "trans")
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for nil client", got)
	}
}

func TestAgents_PropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	client := blocksapi.NewClient(srv.URL, "k")
	if _, err := Agents(context.Background(), client, "trans"); err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}
