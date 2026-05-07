package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/spf13/pflag"
)

// resetPromoteFlags resets all promote command flag variables to defaults
// and clears Cobra's Changed state so cmd.Flags().Changed() is accurate.
func resetPromoteFlags() {
	promoteApiKey = ""
	promoteApiKeyStdin = false
	promoteListing = ""
	promotePrice = ""
	promotePricePerTask = ""
	promotePricePerMinute = ""
	promoteFreeUnits = 0
	promoteFreeTasks = 0
	promoteFreeMinutes = 0
	promoteAcceptTerms = false
	promoteCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
}

func TestPromoteRejectsNonPlaygroundAgent(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"agent": map[string]interface{}{
				"agentName":   "already_public",
				"displayName": "Already Public",
				"listing":     "public",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetPromoteFlags()

	rootCmd.SetArgs([]string{"promote", "already_public", "--listing", "public", "--accept-terms"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-playground agent")
	}
}

func TestPromoteNoArgListsAndSelects(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var patchAgentName string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			// List endpoint: return two playground agents
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
					{
						"agentName":   "weather_01",
						"displayName": "Weather Agent",
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
		} else if r.Method == "PATCH" {
			patchAgentName = r.URL.Query().Get("agentName")
			w.WriteHeader(200)
			w.Write([]byte(`{"agentName":"` + patchAgentName + `","status":"ok","ts":1}`))
		}
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	// Interactive flow: no agent name, no --accept-terms. Stdin feeds
	// selection (2), empty line for optional free-units prompt, then the
	// two attestation prompts (y, y).
	origStdin := os.Stdin
	r, w, _ := os.Pipe()
	w.Write([]byte("2\n\ny\ny\n"))
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = origStdin; r.Close() }()

	resetPromoteFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"promote", "--listing", "public", "--price", "0.15"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("promote failed: %v", err)
		}
	})

	if patchAgentName != "weather_01" {
		t.Errorf("expected PATCH for weather_01 (selection 2), got %q", patchAgentName)
	}
}

func TestPromoteNonInteractiveRequiresAgentName(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	// No backend calls expected — must fail before any HTTP request.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetPromoteFlags()
	rootCmd.SetArgs([]string{"promote", "--listing", "public", "--price", "0.15", "--accept-terms"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --accept-terms used without agent name")
	}
}

func TestPromoteSendsPatchPayload(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var patchReceived map[string]interface{}
	var patchMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			resp := map[string]interface{}{
				"agent": map[string]interface{}{
					"agentName":   "test_agent",
					"displayName": "Test Agent",
					"listing":     "playground",
					"card": map[string]interface{}{
						"capabilities": map[string]interface{}{
							"taskKinds": []string{"request"},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		} else if r.Method == "PATCH" {
			patchMethod = r.Method
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &patchReceived)
			w.WriteHeader(200)
			w.Write([]byte(`{"agentName":"test_agent","status":"ok","ts":1}`))
		}
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetPromoteFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"promote", "test_agent", "--listing", "public", "--price", "0.15", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("promote failed: %v", err)
		}
	})

	if patchMethod != "PATCH" {
		t.Errorf("expected PATCH, got %q", patchMethod)
	}
	if patchReceived["listing"] != "public" {
		t.Errorf("listing = %v, want public", patchReceived["listing"])
	}
	if patchReceived["pricePerTask"] != "0.150000" {
		t.Errorf("pricePerTask = %v, want 0.150000", patchReceived["pricePerTask"])
	}
}

func TestPromoteZeroClearSendsPricePerTaskZero(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var patchReceived map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			resp := map[string]interface{}{
				"agent": map[string]interface{}{
					"agentName":   "test_agent",
					"displayName": "Test Agent",
					"listing":     "playground",
					"card": map[string]interface{}{
						"capabilities": map[string]interface{}{
							"taskKinds": []string{"request"},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		} else if r.Method == "PATCH" {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &patchReceived)
			w.WriteHeader(200)
			w.Write([]byte(`{"agentName":"test_agent","status":"ok","ts":1}`))
		}
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetPromoteFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"promote", "test_agent", "--listing", "private", "--price-per-task", "0", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("promote failed: %v", err)
		}
	})

	if patchReceived["pricePerTask"] != "0.000000" {
		t.Errorf("pricePerTask = %v, want 0.000000 for PATCH-clear", patchReceived["pricePerTask"])
	}
}
