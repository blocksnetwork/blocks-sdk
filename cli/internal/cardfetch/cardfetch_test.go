package cardfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

// newTestServerFromFixture spins up an httptest.Server that returns the
// contents of internal/cardfetch/testdata/<fixture>.json with status code
// at every request, alongside a *blocksapi.Client pointed at it.
func newTestServerFromFixture(t *testing.T, fixture string, status int) (*httptest.Server, *blocksapi.Client) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture+".json"))
	if err != nil {
		t.Fatalf("read fixture %q: %v", fixture, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/registry/agents" {
			t.Errorf("unexpected request path: %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		// Header invariants — see R6.2: Authorization + Blocks-Protocol-Version.
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing/invalid Authorization header: %q", got)
		}
		if got := r.Header.Get("Blocks-Protocol-Version"); got == "" {
			t.Errorf("missing Blocks-Protocol-Version header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	client := blocksapi.NewClient(srv.URL, "test-api-key")
	return srv, client
}

func TestFetch_FullCard_echo2(t *testing.T) {
	_, client := newTestServerFromFixture(t, "echo2", 200)
	card, err := Fetch(context.Background(), client, "echo2")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if card.AgentName != "echo2" {
		t.Errorf("AgentName = %q, want echo2", card.AgentName)
	}
	if got, want := len(card.TaskKinds), 1; got != want {
		t.Fatalf("TaskKinds len = %d, want %d", got, want)
	}
	if card.TaskKinds[0] != "request" {
		t.Errorf("TaskKinds[0] = %q, want request", card.TaskKinds[0])
	}
	if got, want := len(card.Inputs), 1; got != want {
		t.Fatalf("Inputs len = %d, want %d", got, want)
	}
	if card.Inputs[0].ID != "text" {
		t.Errorf("Inputs[0].ID = %q, want text", card.Inputs[0].ID)
	}
	if card.Inputs[0].ContentType != "application/json" {
		t.Errorf("Inputs[0].ContentType = %q", card.Inputs[0].ContentType)
	}
	if !card.Inputs[0].Required {
		t.Errorf("Inputs[0].Required should be true")
	}
	if len(card.Inputs[0].Schema) == 0 {
		t.Errorf("Inputs[0].Schema is empty (expected raw JSON)")
	}
	if len(card.Inputs[0].Example) == 0 {
		t.Errorf("Inputs[0].Example is empty (expected raw JSON)")
	}
	if got, want := len(card.Outputs), 1; got != want {
		t.Fatalf("Outputs len = %d, want %d", got, want)
	}
	if card.Outputs[0].ID != "echoed" {
		t.Errorf("Outputs[0].ID = %q, want echoed", card.Outputs[0].ID)
	}
	if card.Outputs[0].ContentType != "text/plain" {
		t.Errorf("Outputs[0].ContentType = %q", card.Outputs[0].ContentType)
	}
	if !card.Outputs[0].Guaranteed {
		t.Errorf("Outputs[0].Guaranteed should be true")
	}
	if len(card.Streams) != 0 {
		t.Errorf("Streams len = %d, want 0", len(card.Streams))
	}
}

func TestFetch_FullCard_stest1_streams(t *testing.T) {
	_, client := newTestServerFromFixture(t, "stest1", 200)
	card, err := Fetch(context.Background(), client, "stest1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	s, ok := card.Streams["_default"]
	if !ok {
		t.Fatalf("expected stream %q in card.Streams; got %v", "_default", card.Streams)
	}
	if s.Direction != "outbound" {
		t.Errorf("Streams[_default].Direction = %q, want outbound", s.Direction)
	}
	if s.Format != "events" {
		t.Errorf("Streams[_default].Format = %q, want events", s.Format)
	}
}

func TestFetch_PrivateOwned_private_me(t *testing.T) {
	_, client := newTestServerFromFixture(t, "private_me", 200)
	card, err := Fetch(context.Background(), client, "private_me")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if card.AgentName != "private_me" {
		t.Errorf("AgentName = %q, want private_me", card.AgentName)
	}
	// Behavior should be identical shape to a public card: parsed inputs/outputs.
	if len(card.Inputs) != 1 || card.Inputs[0].ID != "request" {
		t.Errorf("expected 1 input id=request, got %+v", card.Inputs)
	}
}

func TestFetch_Minimal(t *testing.T) {
	// 200 with a card that has no io / no streams; expect empty slices, no error.
	_, client := newTestServerFromFixture(t, "minimal", 200)
	card, err := Fetch(context.Background(), client, "minimal")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := len(card.Inputs); got != 0 {
		t.Errorf("Inputs len = %d, want 0", got)
	}
	if got := len(card.Outputs); got != 0 {
		t.Errorf("Outputs len = %d, want 0", got)
	}
	if got := len(card.Streams); got != 0 {
		t.Errorf("Streams len = %d, want 0", got)
	}
}

func TestFetch_404_ReturnsErrAgentNotFound(t *testing.T) {
	_, client := newTestServerFromFixture(t, "not_found", 404)
	_, err := Fetch(context.Background(), client, "doesnotexist")
	if err == nil {
		t.Fatalf("expected error for 404, got nil")
	}
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("err does not wrap ErrAgentNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), "doesnotexist") {
		t.Errorf("error should reference agent name; got %q", err.Error())
	}
}

func TestFetch_NameMismatch(t *testing.T) {
	_, client := newTestServerFromFixture(t, "name_mismatch", 200)
	_, err := Fetch(context.Background(), client, "asked_for")
	if err == nil {
		t.Fatalf("expected mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "asked_for") || !strings.Contains(err.Error(), "actually_other_agent") {
		t.Errorf("error should mention both names; got %q", err.Error())
	}
}

func TestFetch_NetworkError(t *testing.T) {
	// Point at a closed server URL so transport errors fire.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()
	client := blocksapi.NewClient(addr, "test-api-key")
	_, err := Fetch(context.Background(), client, "echo2")
	if err == nil {
		t.Fatalf("expected network error, got nil")
	}
	if !strings.Contains(err.Error(), "echo2") {
		t.Errorf("network error should reference agent name; got %q", err.Error())
	}
}

func TestFetch_ServerError(t *testing.T) {
	// 500 → wrapped *APIError, NOT ErrAgentNotFound.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	client := blocksapi.NewClient(srv.URL, "test-api-key")
	_, err := Fetch(context.Background(), client, "echo2")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if errors.Is(err, ErrAgentNotFound) {
		t.Errorf("500 should NOT be classified as not-found")
	}
}

func TestFetch_BadEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)
	client := blocksapi.NewClient(srv.URL, "test-api-key")
	_, err := Fetch(context.Background(), client, "echo2")
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

func TestFetch_NilClient(t *testing.T) {
	if _, err := Fetch(context.Background(), nil, "x"); err == nil {
		t.Errorf("expected error for nil client")
	}
}

func TestFetch_EmptyAgentName(t *testing.T) {
	client := blocksapi.NewClient("http://example", "x")
	if _, err := Fetch(context.Background(), client, ""); err == nil {
		t.Errorf("expected error for empty agentName")
	}
}

func TestFetch_PassesAgentNameQuery(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("agentName")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agent":{"agentName":"echo2","card":{"capabilities":{"taskKinds":["request"]}}}}`))
	}))
	t.Cleanup(srv.Close)
	client := blocksapi.NewClient(srv.URL, "test-api-key")
	if _, err := Fetch(context.Background(), client, "echo2"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != "echo2" {
		t.Errorf("agentName query = %q, want echo2", got)
	}
}
