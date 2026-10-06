package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
)

func registeredAgentBody(t *testing.T, card map[string]any, listing, billingMode string, pricing map[string]any) []byte {
	t.Helper()
	agent := map[string]any{
		"agentName":   validProjectAgentName,
		"displayName": validProjectAgentName,
		"listing":     listing,
		"tags":        []any{},
		"card":        card,
	}
	if billingMode != "" {
		agent["billingMode"] = billingMode
	}
	for k, v := range pricing {
		agent[k] = v
	}
	body, err := json.Marshal(map[string]any{"agent": agent})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func readCard(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	return card
}

func withDisplayName(card map[string]any, name string) map[string]any {
	clone := map[string]any{}
	for k, v := range card {
		clone[k] = v
	}
	identity := map[string]any{}
	for k, v := range card["identity"].(map[string]any) {
		identity[k] = v
	}
	identity["displayName"] = name
	clone["identity"] = identity
	return clone
}

func TestCheckComparesCardWithRegistry(t *testing.T) {
	type registry struct {
		status      int
		listing     string
		billingMode string
		pricing     map[string]any
		stale       bool
	}
	cases := []struct {
		name     string
		registry *registry // nil: the deployment is unreachable
		noLogin  bool
		want     []string
		wantNot  []string
	}{
		{
			name:     "private free agent is synced with register",
			registry: &registry{status: 200, listing: "private", billingMode: "free", stale: true},
			want:     []string{"[WARN]", "identity.displayName", "`blocks register <path>`", "All checks passed with 1 warning."},
		},
		{
			// register would reset the agent to free; the card is request-only.
			name: "paid agent is synced with publish keeping its listing and pricing",
			registry: &registry{status: 200, listing: "private", billingMode: "paid", stale: true,
				pricing: map[string]any{
					"pricePerTask": "0.050000", "freeTasksPerConsumer": 0,
					"pricePerMinute": "0.000000", "freeMinutesPerConsumer": 0,
				}},
			want: []string{
				"`blocks publish --listing private --billing-mode paid --price-per-task <price-per-task> --free-tasks <free-tasks> <path>`",
				"<price-per-task>  0.050000\n",
				"<free-tasks>  0\n",
			},
			wantNot: []string{"blocks register", "price-per-minute", "free-minutes"},
		},
		{
			name:     "public free agent is synced with publish keeping its listing and billing",
			registry: &registry{status: 200, listing: "public", billingMode: "free", stale: true},
			want:     []string{"[WARN]", "`blocks publish --listing public --billing-mode free <path>`"},
			wantNot:  []string{"To keep its pricing"},
		},
		{
			name:     "an omitted billing mode is not assumed free",
			registry: &registry{status: 200, listing: "private", stale: true},
			want:     []string{"[WARN]", "`blocks publish <path>`"},
			wantNot:  []string{"blocks register"},
		},
		{
			name:     "matching card reports no drift",
			registry: &registry{status: 200, listing: "public", billingMode: "free"},
			want:     []string{"[OK] Card matches the version registered", "All checks passed.\n"},
			wantNot:  []string{"[WARN]"},
		},
		{
			name:     "unregistered agent is not an error",
			registry: &registry{status: 404},
			want:     []string{"[INFO] test_agent is not registered", "`blocks register <path>`", "All checks passed.\n"},
		},
		{
			name: "unreachable deployment skips the comparison",
			want: []string{"[SKIP] Registry comparison skipped", "All checks passed.\n"},
		},
		{
			// The server is also the remote config, so resolving the deployment counts too.
			name:     "no login skips the comparison without a request",
			registry: &registry{status: 200, listing: "public", billingMode: "free", stale: true},
			noLogin:  true,
			want:     []string{"[SKIP] Registry comparison skipped: not logged in", "All checks passed.\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreCLIState(t)
			defer isolateProfiles(t)()
			isolateAmbientState(t)

			cardPath := filepath.Join(writeValidProject(t), "agent-card.json")
			local := readCard(t, cardPath)

			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				reg := tc.registry
				if reg.status != http.StatusOK {
					http.Error(w, `{"error":"not found"}`, reg.status)
					return
				}
				card := local
				if reg.stale {
					card = withDisplayName(local, "Old Name")
				}
				w.Write(registeredAgentBody(t, card, reg.listing, reg.billingMode, reg.pricing))
			}))
			if tc.registry == nil {
				srv.Close()
			} else {
				t.Cleanup(srv.Close)
			}
			if tc.noLogin {
				t.Setenv("BLOCKS_CDM_URL", srv.URL)
			} else {
				t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
				t.Setenv("BLOCKS_API_KEY", "bk_test")
			}

			var runErr error
			out := captureStdoutStderr(func() {
				rootCmd.SetArgs([]string{"check", cardPath})
				runErr = rootCmd.Execute()
			})

			if runErr != nil {
				t.Fatalf("check failed: %v\n%s", runErr, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(out, w) {
					t.Errorf("output contains %q:\n%s", w, out)
				}
			}
			if tc.noLogin && requests.Load() != 0 {
				t.Errorf("made %d request(s) without a credential", requests.Load())
			}
		})
	}
}

func TestSyncCommandCarriesPricingOnlyWhileItStillPricesTheCard(t *testing.T) {
	registeredRequestAgent := &cardfetch.AgentCard{
		Listing:     "public",
		BillingMode: "paid",
		Pricing:     cardfetch.Pricing{PricePerTask: "0.050000", PricePerMinute: "0.000000"},
	}
	cardWithKinds := func(kinds ...any) map[string]any {
		return map[string]any{"capabilities": map[string]any{"taskKinds": kinds}}
	}
	cases := []struct {
		name           string
		card           map[string]any
		wantCommand    string
		wantNewPricing bool
	}{
		{
			name:        "unchanged kinds keep the registered price",
			card:        cardWithKinds("request"),
			wantCommand: "blocks publish --listing public --billing-mode paid --price-per-task <price-per-task>",
		},
		{
			name:           "a kind the registered pricing does not price leaves pricing to publish",
			card:           cardWithKinds("pipe"),
			wantCommand:    "blocks publish --listing public --billing-mode paid",
			wantNewPricing: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := syncCommand(tc.card, registeredRequestAgent)
			if got.command != tc.wantCommand || got.needsNewPricing != tc.wantNewPricing {
				t.Errorf("syncCommand = %q (needsNewPricing %v), want %q (%v)",
					got.command, got.needsNewPricing, tc.wantCommand, tc.wantNewPricing)
			}
		})
	}
}
