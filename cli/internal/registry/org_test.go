package registry

import (
	"bufio"
	"strings"
	"testing"
)

func TestPromptOrgNameSkipsWhenContextNil(t *testing.T) {
	name, err := PromptOrgName(nil, OrgNameFlags{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
}

func TestPromptOrgNameSkipsWhenAgentCountPositive(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "My Org", AgentCount: 3}
	name, err := PromptOrgName(ctx, OrgNameFlags{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
}

func TestPromptOrgNameFlagHonoredWithPositiveAgentCount(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "Old Name", AgentCount: 5}
	flagVal := "Explicit Name"
	flags := OrgNameFlags{OrgName: &flagVal}
	name, err := PromptOrgName(ctx, flags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Explicit Name" {
		t.Errorf("expected %q, got %q", "Explicit Name", name)
	}
}

func TestPromptOrgNameUsesFlag(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "Old Name", AgentCount: 0}
	flagVal := "New Org"
	flags := OrgNameFlags{OrgName: &flagVal}
	name, err := PromptOrgName(ctx, flags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "New Org" {
		t.Errorf("expected %q, got %q", "New Org", name)
	}
}

func TestPromptOrgNameNonInteractiveNoFlag(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "Old Name", AgentCount: 0}
	flags := OrgNameFlags{NonInteractive: true}
	name, err := PromptOrgName(ctx, flags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "" {
		t.Errorf("expected empty (keep current), got %q", name)
	}
}

func TestPromptOrgNameInteractiveInput(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "Old Name", AgentCount: 0}
	scanner := bufio.NewScanner(strings.NewReader("Acme Corp\n"))
	name, err := PromptOrgName(ctx, OrgNameFlags{}, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Acme Corp" {
		t.Errorf("expected %q, got %q", "Acme Corp", name)
	}
}

func TestPromptOrgNameInteractiveBlank(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "Old Name", AgentCount: 0}
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	name, err := PromptOrgName(ctx, OrgNameFlags{}, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "" {
		t.Errorf("expected empty (keep current), got %q", name)
	}
}

func TestPromptOrgNameHelpThenInput(t *testing.T) {
	ctx := &PublishContext{OrgID: "org1", OrgName: "Old Name", AgentCount: 0}
	scanner := bufio.NewScanner(strings.NewReader("?\nMy Org\n"))
	name, err := PromptOrgName(ctx, OrgNameFlags{}, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "My Org" {
		t.Errorf("expected %q, got %q", "My Org", name)
	}
}
