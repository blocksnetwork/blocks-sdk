package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFetchLatestNpmVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/@blocks-network/cli/latest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(npmPackageVersion{Version: "0.2.0"})
	}))
	defer srv.Close()

	orig := npmRegistry
	npmRegistryURL = srv.URL
	defer func() { npmRegistryURL = orig }()

	ver, err := fetchLatestNpmVersion()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "0.2.0" {
		t.Errorf("version = %q, want %q", ver, "0.2.0")
	}
}

func TestFetchLatestNpmVersion_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := npmRegistry
	npmRegistryURL = srv.URL
	defer func() { npmRegistryURL = orig }()

	_, err := fetchLatestNpmVersion()
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPlatformPackage(t *testing.T) {
	pkg, err := platformPackage()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	key := runtime.GOOS + "/" + runtime.GOARCH
	want := npmPlatformPackages[key]
	if pkg != want {
		t.Errorf("platformPackage() = %q, want %q", pkg, want)
	}
}

func TestVerifyIntegrity_Valid(t *testing.T) {
	data := []byte("hello world tarball content")
	digest := sha512.Sum512(data)
	sri := "sha512-" + base64.StdEncoding.EncodeToString(digest[:])

	if err := verifyIntegrity(data, sri); err != nil {
		t.Fatalf("expected no error for valid hash, got: %v", err)
	}
}

func TestVerifyIntegrity_Mismatch(t *testing.T) {
	data := []byte("legitimate tarball")
	sri := "sha512-" + base64.StdEncoding.EncodeToString([]byte("not-a-real-hash-value-padding-needed-for-test"))

	err := verifyIntegrity(data, sri)
	if err == nil {
		t.Fatal("expected error for mismatched hash")
	}
	if !strings.Contains(err.Error(), "integrity check failed") {
		t.Errorf("error should mention integrity check, got: %v", err)
	}
}

func TestVerifyIntegrity_Empty(t *testing.T) {
	err := verifyIntegrity([]byte("data"), "")
	if err == nil {
		t.Fatal("expected error when integrity string is empty")
	}
}

func TestVerifyIntegrity_UnsupportedAlgo(t *testing.T) {
	err := verifyIntegrity([]byte("data"), "sha256-abc123")
	if err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
	if !strings.Contains(err.Error(), "unsupported algorithm") {
		t.Errorf("error should mention unsupported algorithm, got: %v", err)
	}
}

func TestIsNpmManagedPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{filepath.Join("/opt/homebrew/lib/node_modules/@blocks-network/cli-darwin-arm64/blocks"), true},
		{filepath.Join("/home/user/.npm-global/lib/node_modules/@blocks-network/cli-linux-x64/blocks"), true},
		{filepath.Join("/home/user/.blocks/bin/blocks"), false},
		{filepath.Join("/usr/local/bin/blocks"), false},
	}

	for _, tt := range tests {
		if got := isNpmManagedPath(tt.path); got != tt.want {
			t.Errorf("isNpmManagedPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestResolveInstallDir_NpmManaged(t *testing.T) {
	// Create a temp dir mimicking an npm-global install layout
	tmp := t.TempDir()
	npmBinDir := filepath.Join(tmp, "lib", "node_modules", "@blocks-network", "cli-darwin-arm64")
	os.MkdirAll(npmBinDir, 0o755)
	fakeBinary := filepath.Join(npmBinDir, "blocks")
	os.WriteFile(fakeBinary, []byte("#!/bin/sh\n"), 0o755)

	// isNpmManagedPath should detect this
	if !isNpmManagedPath(fakeBinary) {
		t.Fatal("expected npm-managed path to be detected")
	}
}

func TestResolveInstallDir_EnvOverride(t *testing.T) {
	t.Setenv("BLOCKS_INSTALL_DIR", "/custom/path")
	dir, err := resolveInstallDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "/custom/path" {
		t.Errorf("resolveInstallDir() = %q, want %q", dir, "/custom/path")
	}
}

func buildTestTarball(t *testing.T, fileName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     "package/" + fileName,
		Mode:     0o755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestExtractBinaryFromTarball_HappyPath(t *testing.T) {
	content := []byte("fake binary content")
	tarball := buildTestTarball(t, "blocks", content)

	result, err := extractBinaryFromTarball(bytes.NewReader(tarball), "blocks")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(result, content) {
		t.Errorf("extracted content doesn't match: got %d bytes, want %d", len(result), len(content))
	}
}

func TestExtractBinaryFromTarball_NotFound(t *testing.T) {
	tarball := buildTestTarball(t, "other-file", []byte("data"))

	_, err := extractBinaryFromTarball(bytes.NewReader(tarball), "blocks")
	if err == nil {
		t.Fatal("expected error when binary not in tarball")
	}
	if !strings.Contains(err.Error(), "not found in tarball") {
		t.Errorf("error should mention not found, got: %v", err)
	}
}

func TestDownloadAndInstall_FullPipeline(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho hello\n")
	tarball := buildTestTarball(t, "blocks", binaryContent)

	digest := sha512.Sum512(tarball)
	sri := "sha512-" + base64.StdEncoding.EncodeToString(digest[:])

	_, err := platformPackage()
	if err != nil {
		t.Fatalf("platformPackage: %v", err)
	}

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/@blocks-network/cli/latest":
			json.NewEncoder(w).Encode(npmPackageVersion{Version: "1.0.0"})
		case strings.HasSuffix(r.URL.Path, "/1.0.0"):
			meta := npmPackageVersion{Version: "1.0.0"}
			meta.Dist.Tarball = srvURL + "/tarball.tgz"
			meta.Dist.Integrity = sri
			json.NewEncoder(w).Encode(meta)
		case r.URL.Path == "/tarball.tgz":
			w.Write(tarball)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	orig := npmRegistryURL
	npmRegistryURL = srv.URL
	defer func() { npmRegistryURL = orig }()

	installDir := t.TempDir()
	t.Setenv("BLOCKS_INSTALL_DIR", installDir)

	err = downloadAndInstall("1.0.0")
	if err != nil {
		t.Fatalf("downloadAndInstall failed: %v", err)
	}

	binaryName := "blocks"
	if runtime.GOOS == "windows" {
		binaryName = "blocks.exe"
	}
	installed, err := os.ReadFile(filepath.Join(installDir, binaryName))
	if err != nil {
		t.Fatalf("installed binary not found: %v", err)
	}
	if !bytes.Equal(installed, binaryContent) {
		t.Error("installed binary content doesn't match expected")
	}
}

func TestDownloadAndInstall_IntegrityMismatch(t *testing.T) {
	tarball := buildTestTarball(t, "blocks", []byte("binary"))
	wrongSRI := "sha512-" + base64.StdEncoding.EncodeToString([]byte("wrong-hash-padding-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/1.0.0"):
			meta := npmPackageVersion{Version: "1.0.0"}
			meta.Dist.Tarball = srvURL + "/tarball.tgz"
			meta.Dist.Integrity = wrongSRI
			json.NewEncoder(w).Encode(meta)
		case r.URL.Path == "/tarball.tgz":
			w.Write(tarball)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	orig := npmRegistryURL
	npmRegistryURL = srv.URL
	defer func() { npmRegistryURL = orig }()

	t.Setenv("BLOCKS_INSTALL_DIR", t.TempDir())

	err := downloadAndInstall("1.0.0")
	if err == nil {
		t.Fatal("expected integrity error")
	}
	if !strings.Contains(err.Error(), "integrity check failed") {
		t.Errorf("error should mention integrity, got: %v", err)
	}
}
