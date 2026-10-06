package wizard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIsHelpRequest(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"?", true},
		{"  ?  ", true},
		{"?name", true},
		{"", false},
		{"name", false},
		{"name?", false},
	}
	for _, tc := range cases {
		if got := IsHelpRequest(tc.in); got != tc.want {
			t.Errorf("IsHelpRequest(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestExpectedInstancesMinimumMatchesAgentCardSchema(t *testing.T) {
	// blocks-sdk/schemas (canonical) and the CLI's embedded copy of it.
	for _, path := range []string{"../../../schemas/agent-card.schema.json", "../schema/schemas/agent-card.schema.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		minimums := findPropertyMinimums(doc, "expectedInstances")
		if len(minimums) == 0 {
			t.Fatalf("%s: no expectedInstances minimum found", path)
		}
		for _, m := range minimums {
			if m != float64(minExpectedInstances) {
				t.Errorf("%s: expectedInstances minimum = %v, wizard minExpectedInstances = %d", path, m, minExpectedInstances)
			}
		}
	}
	if !strings.Contains(helpInstances, "0 is broadcast") {
		t.Error("expected-instances help must explain the schema minimum, 0")
	}
}

func findPropertyMinimums(node any, property string) []float64 {
	var out []float64
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if key == property {
				if def, ok := child.(map[string]any); ok {
					if m, ok := def["minimum"].(float64); ok {
						out = append(out, m)
					}
				}
			}
			out = append(out, findPropertyMinimums(child, property)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, findPropertyMinimums(child, property)...)
		}
	}
	return out
}
