package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

// TestMakeAgentSuggestFn verifies the cmd→wizard suggestion adapter maps
// agentName→Value and displayName→Label.
func TestMakeAgentSuggestFn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/registry/suggest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agents":[{"agentName":"translator","displayName":"Acme Translator"}]}`))
	}))
	defer srv.Close()

	fn := makeAgentSuggestFn(blocksapi.NewClient(srv.URL, "k"))
	got, err := fn(context.Background(), "trans")
	if err != nil {
		t.Fatalf("suggest fn: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1", len(got))
	}
	if got[0].Value != "translator" || got[0].Label != "Acme Translator" {
		t.Errorf("suggestion = %+v, want {translator, Acme Translator}", got[0])
	}
}

// TestSelectDeployTargetNonTTY verifies the deploy picker returns no selection
// when stdin is not a terminal, so callers fall through to the "no target"
// error rather than silently picking the first registered target.
func TestSelectDeployTargetNonTTY(t *testing.T) {
	got, err := selectDeployTarget("")
	if err != nil {
		t.Fatalf("selectDeployTarget: %v", err)
	}
	if got != "" {
		t.Errorf("selectDeployTarget() = %q, want \"\" on non-terminal stdin", got)
	}
}

// TestInitNoArgsDoesNotHang guards the regression where `blocks init` with no
// args under a non-terminal stdin entered an interactive loop that spun on EOF.
// It must return promptly with an error (the agent name is required), never hang.
func TestInitNoArgsDoesNotHang(t *testing.T) {
	resetInitFlags()
	rootCmd.SetArgs([]string{"init"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error for `blocks init` with no args on non-terminal stdin")
	}
}
