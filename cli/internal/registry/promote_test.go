package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromoteSendsCorrectPayload(t *testing.T) {
	var received map[string]interface{}
	var receivedQuery string
	var receivedMethod string
	var receivedAuth string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedQuery = r.URL.Query().Get("agentName")
		receivedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"agentName":"test_agent","status":"ok","ts":1}`))
	}))
	defer ts.Close()

	price := "0.150000"
	free := 5
	input := PromotionInput{
		Listing:              "public",
		PricePerTask:         &price,
		FreeTasksPerConsumer: &free,
		TcAcceptedAt:         "2026-04-16T10:00:00.000Z",
	}

	err := Promote(context.Background(), ts.URL, "bk_test_key", "test_agent", input)
	if err != nil {
		t.Fatalf("Promote() error: %v", err)
	}

	if receivedMethod != "PATCH" {
		t.Errorf("method = %q, want PATCH", receivedMethod)
	}
	if receivedQuery != "test_agent" {
		t.Errorf("agentName query = %q, want test_agent", receivedQuery)
	}
	if receivedAuth != "Bearer bk_test_key" {
		t.Errorf("auth = %q, want Bearer bk_test_key", receivedAuth)
	}
	if received["listing"] != "public" {
		t.Errorf("listing = %v, want public", received["listing"])
	}
	if received["pricePerTask"] != "0.150000" {
		t.Errorf("pricePerTask = %v, want 0.150000", received["pricePerTask"])
	}
	if received["tcAcceptedAt"] != "2026-04-16T10:00:00.000Z" {
		t.Errorf("tcAcceptedAt = %v", received["tcAcceptedAt"])
	}
}

func TestPromoteReturnsErrorOn401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	input := PromotionInput{Listing: "public", TcAcceptedAt: "2026-04-16T10:00:00.000Z"}
	err := Promote(context.Background(), ts.URL, "bad_key", "test_agent", input)
	if err == nil {
		t.Fatal("expected error on 401")
	}
}
