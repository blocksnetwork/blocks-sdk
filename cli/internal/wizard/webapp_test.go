package wizard

import (
	"bufio"
	"strings"
	"testing"
)

func TestValidateProjectName(t *testing.T) {
	valid := []string{"myapp", "my-app", "my_app", "app.v2", "App123"}
	for _, n := range valid {
		if err := ValidateProjectName(n); err != nil {
			t.Errorf("ValidateProjectName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", ".", "..", "my app", "a/b", "a\\b", "café"}
	for _, n := range invalid {
		if err := ValidateProjectName(n); err == nil {
			t.Errorf("ValidateProjectName(%q) = nil, want error", n)
		}
	}
}

func TestCollectAgentsPlain(t *testing.T) {
	// Two valid agents, an invalid one (rejected), a duplicate (skipped),
	// then a blank line to finish.
	in := "translator\nbad/name\ntranslator\nsummarize\n\n"
	r := bufio.NewReader(strings.NewReader(in))
	agents, err := collectAgentsPlain(r)
	if err != nil {
		t.Fatalf("collectAgentsPlain: %v", err)
	}
	want := []string{"translator", "summarize"}
	if len(agents) != len(want) {
		t.Fatalf("agents = %v, want %v", agents, want)
	}
	for i := range want {
		if agents[i] != want[i] {
			t.Errorf("agents[%d] = %q, want %q", i, agents[i], want[i])
		}
	}
}

func TestCollectAgentsPlain_RequiresAtLeastOne(t *testing.T) {
	// First blank is rejected (need ≥1); then a valid name; then blank to finish.
	in := "\ntranslator\n\n"
	r := bufio.NewReader(strings.NewReader(in))
	agents, err := collectAgentsPlain(r)
	if err != nil {
		t.Fatalf("collectAgentsPlain: %v", err)
	}
	if len(agents) != 1 || agents[0] != "translator" {
		t.Fatalf("agents = %v, want [translator]", agents)
	}
}
