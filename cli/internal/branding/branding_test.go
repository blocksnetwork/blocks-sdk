package branding

import "testing"

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
