package branding

import (
	"strings"
	"testing"
)

func TestDefaultAndOverride(t *testing.T) {
	Reset()
	if ProductName() != "Blocks Network" {
		t.Fatalf("default product name, got %q", ProductName())
	}
	Set("Acme AI Hub")
	if ProductName() != "Acme AI Hub" {
		t.Fatalf("override, got %q", ProductName())
	}
	Reset()
	if ProductName() != "Blocks Network" {
		t.Fatalf("reset, got %q", ProductName())
	}
}

func TestSetEmptyIsIgnored(t *testing.T) {
	Reset()
	Set("")
	if ProductName() != "Blocks Network" {
		t.Fatalf("empty Set must not clobber default, got %q", ProductName())
	}
	Set("Acme AI Hub")
	Set("")
	if ProductName() != "Acme AI Hub" {
		t.Fatalf("empty Set must not clobber a prior value, got %q", ProductName())
	}
	Reset()
}

// The product name is deployment-controlled — it comes from the profile's cached copy
// or the cli-config discovery response — and it is interpolated into prompts, banners,
// help text and success lines. Escaping it at the point it enters the package is what
// makes every one of those safe without each print site remembering; this pins that,
// so a change that moves the escape back out to the callers fails here.
func TestSetEscapesADeploymentControlledName(t *testing.T) {
	t.Cleanup(Reset)

	// A name that erases the line above it and rewrites what the user just read.
	Set("Acme\x1b[2K\r\x1b[1AHarmless Corporation")

	got := ProductName()
	if strings.ContainsAny(got, "\x1b\r") {
		t.Errorf("ProductName() still carries control characters: %q", got)
	}
	if !strings.Contains(got, "Acme") || !strings.Contains(got, "Harmless Corporation") {
		t.Errorf("the visible text must survive escaping, got %q", got)
	}
	if !strings.Contains(got, `\x1b`) {
		t.Errorf("the escape must stay visible rather than being stripped, got %q", got)
	}

	// Idempotent: a name that is already escaped is not escaped twice.
	once := ProductName()
	Set(once)
	if ProductName() != once {
		t.Errorf("escaping is not idempotent: %q became %q", once, ProductName())
	}
}
