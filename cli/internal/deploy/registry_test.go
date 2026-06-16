package deploy

import (
	"context"
	"sort"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

func stubUpload(ctx context.Context, creds *auth.ProviderCredentials, dir string) (string, error) {
	return "https://stub.example.com", nil
}

func TestBuiltinsAreRegistered(t *testing.T) {
	Reset()
	all := List()
	names := make([]string, 0, len(all))
	for _, a := range all {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	want := []string{"cloudflare", "netlify", "vercel"}
	if len(names) != len(want) {
		t.Fatalf("List() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestResolveBuiltin(t *testing.T) {
	Reset()
	a, ok := Resolve("cloudflare")
	if !ok {
		t.Fatal("cloudflare not registered")
	}
	if a.Source != SourceBuiltin {
		t.Errorf("Source = %v, want %v", a.Source, SourceBuiltin)
	}
	if a.Upload == nil {
		t.Error("Upload func is nil")
	}
}

func TestResolveUnknown(t *testing.T) {
	Reset()
	if _, ok := Resolve("nonesuch"); ok {
		t.Error("Resolve(nonesuch) should return ok=false")
	}
}

// TestRegisterDiskOverridesBuiltin: a disk-source adapter with the same name
// as a built-in wins. Users dropping a YAML file with the same name should
// get their script, not the built-in.
func TestRegisterDiskOverridesBuiltin(t *testing.T) {
	Reset()
	Register(Adapter{
		Name:   "cloudflare",
		Source: SourceDisk,
		Upload: stubUpload,
	})
	a, _ := Resolve("cloudflare")
	if a.Source != SourceDisk {
		t.Errorf("disk did not override built-in: source = %v", a.Source)
	}
}

// TestRegisterBuiltinDoesNotOverrideDisk: a built-in registered after a disk
// adapter of the same name must NOT clobber the disk override.
func TestRegisterBuiltinDoesNotOverrideDisk(t *testing.T) {
	Reset()
	Register(Adapter{Name: "cloudflare", Source: SourceDisk, Upload: stubUpload})
	Register(Adapter{Name: "cloudflare", Source: SourceBuiltin, Upload: CloudflareUpload})

	a, _ := Resolve("cloudflare")
	if a.Source != SourceDisk {
		t.Errorf("built-in clobbered disk override; source = %v", a.Source)
	}
}

func TestRegisterNewName(t *testing.T) {
	Reset()
	Register(Adapter{Name: "railway", Source: SourceDisk, Upload: stubUpload, Description: "test"})
	a, ok := Resolve("railway")
	if !ok {
		t.Fatal("railway not registered")
	}
	if a.Description != "test" {
		t.Errorf("description = %q, want %q", a.Description, "test")
	}
}
