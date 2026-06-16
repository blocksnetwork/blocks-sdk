package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// collectFiles must read regular files, follow in-tree symlinks, and refuse
// any symlink whose target escapes the deploy root (an exfiltration vector
// since the result is uploaded to a public CDN).

func TestCollectFiles_RegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("x=1"), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := collectFiles(root)
	if err != nil {
		t.Fatalf("collectFiles: %v", err)
	}
	if string(files["index.html"]) != "<html>" {
		t.Errorf("index.html = %q, want %q", files["index.html"], "<html>")
	}
	if string(files["assets/app.js"]) != "x=1" {
		t.Errorf("assets/app.js = %q, want %q", files["assets/app.js"], "x=1")
	}
}

func TestCollectFiles_InTreeSymlink_Included(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	files, err := collectFiles(root)
	if err != nil {
		t.Fatalf("collectFiles: %v", err)
	}
	if string(files["link.txt"]) != "data" {
		t.Errorf("link.txt = %q, want %q (in-tree symlink should be followed)", files["link.txt"], "data")
	}
}

func TestCollectFiles_RelativeEscapeSymlink_Errors(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, "secret.env")
	if err := os.WriteFile(secret, []byte("API_KEY=shh"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "web")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	// web/leak -> ../secret.env  (escapes root via "..")
	if err := os.Symlink(filepath.Join("..", "secret.env"), filepath.Join(root, "leak")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	_, err := collectFiles(root)
	if err == nil {
		t.Fatal("expected error for symlink escaping the deploy root, got nil")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %q, want it to mention the target escaping the deploy directory", err.Error())
	}
}

func TestCollectFiles_AbsoluteEscapeSymlink_Errors(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(target, []byte("PRIVATE"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	// web/key -> /abs/path/outside/id_rsa
	if err := os.Symlink(target, filepath.Join(root, "key")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	_, err := collectFiles(root)
	if err == nil {
		t.Fatal("expected error for absolute symlink escaping the deploy root, got nil")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %q, want it to mention the target escaping the deploy directory", err.Error())
	}
}

func TestCollectFiles_DanglingSymlink_Errors(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "nonexistent"), filepath.Join(root, "broken")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	_, err := collectFiles(root)
	if err == nil {
		t.Fatal("expected error for dangling symlink, got nil")
	}
}

func TestCollectFiles_InTreeDirSymlink_Skipped(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "realdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "realdir", "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	// web/linkdir -> realdir (in-tree directory symlink)
	if err := os.Symlink(filepath.Join(root, "realdir"), filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	files, err := collectFiles(root)
	if err != nil {
		t.Fatalf("collectFiles: %v", err)
	}
	if string(files["realdir/a.txt"]) != "a" {
		t.Errorf("realdir/a.txt missing; got %q", files["realdir/a.txt"])
	}
	if _, ok := files["linkdir"]; ok {
		t.Errorf("directory symlink should be skipped, not added as a file entry")
	}
	if _, ok := files["linkdir/a.txt"]; ok {
		t.Errorf("directory symlink should not be expanded")
	}
}

func TestCollectFiles_EscapingDirSymlink_Errors(t *testing.T) {
	base := t.TempDir()
	// a directory OUTSIDE the deploy root
	outsideDir := filepath.Join(base, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "leak.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "web")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	// web/linkdir -> ../outside  (a directory symlink that escapes the root)
	if err := os.Symlink(filepath.Join("..", "outside"), filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	_, err := collectFiles(root)
	if err == nil {
		t.Fatal("expected error for directory symlink escaping the deploy root, got nil")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %q, want it to mention the target escaping the deploy directory", err.Error())
	}
}
