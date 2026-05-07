package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchPlaygroundAgents(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Query().Get("scope") != "owned" {
			t.Errorf("expected scope=owned, got %q", r.URL.Query().Get("scope"))
		}
		if r.URL.Query().Get("listing") != "playground" {
			t.Errorf("expected listing=playground, got %q", r.URL.Query().Get("listing"))
		}
		if r.URL.Query().Get("include") != "full" {
			t.Errorf("expected include=full, got %q", r.URL.Query().Get("include"))
		}

		resp := map[string]interface{}{
			"agents": []map[string]interface{}{
				{
					"agentName":   "echo_001",
					"displayName": "Echo Agent",
					"listing":     "playground",
					"card": map[string]interface{}{
						"capabilities": map[string]interface{}{
							"taskKinds": []string{"request"},
						},
					},
				},
			},
			"next": nil,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	agents, err := FetchPlaygroundAgents(context.Background(), ts.URL, "bk_test")
	if err != nil {
		t.Fatalf("FetchPlaygroundAgents() error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].AgentName != "echo_001" {
		t.Errorf("agentName = %q, want echo_001", agents[0].AgentName)
	}
	if agents[0].IsStreaming() {
		t.Error("expected IsStreaming()=false for request agent")
	}
}

func TestFetchPlaygroundAgentsPagination(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		cursor := r.URL.Query().Get("cursor")
		if cursor == "" {
			// First page
			resp := map[string]interface{}{
				"agents": []map[string]interface{}{
					{"agentName": "agent_1", "displayName": "A1", "listing": "playground"},
				},
				"next": "cursor_page2",
			}
			json.NewEncoder(w).Encode(resp)
		} else {
			// Second page
			resp := map[string]interface{}{
				"agents": []map[string]interface{}{
					{"agentName": "agent_2", "displayName": "A2", "listing": "playground"},
				},
				"next": nil,
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer ts.Close()

	agents, err := FetchPlaygroundAgents(context.Background(), ts.URL, "bk_test")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents across pages, got %d", len(agents))
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls (pagination), got %d", callCount)
	}
}

func TestFetchAgent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("agentName") != "echo_001" {
			t.Errorf("expected agentName=echo_001")
		}
		resp := map[string]interface{}{
			"agent": map[string]interface{}{
				"agentName":   "echo_001",
				"displayName": "Echo Agent",
				"listing":     "playground",
				"card": map[string]interface{}{
					"capabilities": map[string]interface{}{
						"taskKinds": []string{"pipe"},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	agent, err := FetchAgent(context.Background(), ts.URL, "bk_test", "echo_001")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if agent.AgentName != "echo_001" {
		t.Errorf("agentName = %q", agent.AgentName)
	}
	if !agent.IsStreaming() {
		t.Error("expected IsStreaming()=true for pipe agent")
	}
}
