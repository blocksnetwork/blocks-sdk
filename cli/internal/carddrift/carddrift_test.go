package carddrift

import (
	"encoding/json"
	"slices"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return m
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name       string
		local      string
		registered string
		want       []string
	}{
		{
			name:       "key order and number formatting are not drift",
			local:      `{"identity":{"agentName":"a","displayName":"A"},"runtime":{"concurrency":2}}`,
			registered: `{"runtime":{"concurrency":2.0},"identity":{"displayName":"A","agentName":"a"}}`,
			want:       nil,
		},
		{
			name:       "changed nested scalar is named by its full path",
			local:      `{"identity":{"agentName":"a","displayName":"New"}}`,
			registered: `{"identity":{"agentName":"a","displayName":"Old"}}`,
			want:       []string{"identity.displayName"},
		},
		{
			name:       "fields present on only one side are reported from either side",
			local:      `{"identity":{"description":"d"},"io":{}}`,
			registered: `{"identity":{},"security":{}}`,
			want:       []string{"identity.description", "io", "security"},
		},
		{
			name:       "arrays are reported at the array path, not per index",
			local:      `{"tags":[{"id":"x","name":"X"},{"id":"y","name":"Y"}]}`,
			registered: `{"tags":[{"id":"y","name":"Y"},{"id":"x","name":"X"}]}`,
			want:       []string{"tags"},
		},
		{
			name:       "an object replaced by a scalar is one difference",
			local:      `{"extensions":{"k":1}}`,
			registered: `{"extensions":"k"}`,
			want:       []string{"extensions"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(decode(t, tc.local), decode(t, tc.registered))
			if !slices.Equal(got, tc.want) {
				t.Errorf("Diff = %v, want %v", got, tc.want)
			}
		})
	}
}
