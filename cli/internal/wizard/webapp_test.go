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

func TestRemoveString(t *testing.T) {
	got := removeString([]string{"a", "b", "c"}, "b")
	want := []string{"a", "c"}
	if len(got) != len(want) || got[0] != "a" || got[1] != "c" {
		t.Errorf("removeString = %v, want %v", got, want)
	}
	if got := removeString([]string{"a"}, "a"); len(got) != 0 {
		t.Errorf("removeString to empty = %v, want empty", got)
	}
	if got := removeString([]string{"a"}, "z"); len(got) != 1 {
		t.Errorf("removeString no-match = %v, want [a]", got)
	}
}

func TestReviewAgentsPlain(t *testing.T) {
	// A name not in the list is rejected; weather_service is removed; the
	// blank line finishes the review.
	in := "not_in_list\nweather_service\n\n"
	r := bufio.NewReader(strings.NewReader(in))
	agents, err := reviewAgentsPlain(r, []string{"weather_service", "translator"})
	if err != nil {
		t.Fatalf("reviewAgentsPlain: %v", err)
	}
	if len(agents) != 1 || agents[0] != "translator" {
		t.Fatalf("agents = %v, want [translator]", agents)
	}
}

func TestReviewAgentsPlain_BlankKeepsEverything(t *testing.T) {
	in := "\n"
	r := bufio.NewReader(strings.NewReader(in))
	agents, err := reviewAgentsPlain(r, []string{"weather_service"})
	if err != nil {
		t.Fatalf("reviewAgentsPlain: %v", err)
	}
	if len(agents) != 1 || agents[0] != "weather_service" {
		t.Fatalf("agents = %v, want [weather_service]", agents)
	}
}

func TestReviewAgentsPlain_RemoveAllThenBlank(t *testing.T) {
	// Removing the last agent exits the loop (len == 0) without reading
	// another line — the caller decides what an empty list means.
	in := "weather_service\n"
	r := bufio.NewReader(strings.NewReader(in))
	agents, err := reviewAgentsPlain(r, []string{"weather_service"})
	if err != nil {
		t.Fatalf("reviewAgentsPlain: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("agents = %v, want empty", agents)
	}
}

func TestCollectAndReviewAgentsPlain_RestartsAfterRemovingAll(t *testing.T) {
	// Collect two agents, blank to finish collection, remove both, then the
	// restart collects one more and the review keeps it.
	in := "weather_service\ntranslator\n\nweather_service\ntranslator\nsummarize\n\n\n"
	r := bufio.NewReader(strings.NewReader(in))
	agents, err := collectAndReviewAgentsPlain(r)
	if err != nil {
		t.Fatalf("collectAndReviewAgentsPlain: %v", err)
	}
	if len(agents) != 1 || agents[0] != "summarize" {
		t.Fatalf("agents = %v, want [summarize]", agents)
	}
}
