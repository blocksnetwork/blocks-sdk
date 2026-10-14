package agentname

import "testing"

func TestSuggest(t *testing.T) {
	cases := map[string]string{
		"my-agent":        "my_agent",
		"acme.echo":       "acme_echo",
		"my agent":        "my_agent",
		"acme/translator": "acme_translator",
		"--my--agent--":   "my_agent",
		"a__b-c":          "a_b_c",
		"café-bot":        "caf_bot",
		"---":             "",
		"":                "",
	}
	for in, want := range cases {
		got := Suggest(in)
		if got != want {
			t.Errorf("Suggest(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !Pattern.MatchString(got) {
			t.Errorf("Suggest(%q) = %q does not pass Pattern", in, got)
		}
	}
}

func TestHintOmitsAnEmptySuggestion(t *testing.T) {
	if got := Hint("---"); got != "" {
		t.Errorf("Hint(%q) = %q, want empty", "---", got)
	}
	if got, want := Hint("my-agent"), ` — try "my_agent"`; got != want {
		t.Errorf("Hint(%q) = %q, want %q", "my-agent", got, want)
	}
}
