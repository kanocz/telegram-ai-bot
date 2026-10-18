package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// sandboxWithSymlink builds a sandbox root holding "link", a symlink pointing
// at a sibling directory outside the sandbox. Returns the resolved root and
// the outside directory.
func sandboxWithSymlink(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "sandbox")
	outside = filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	return realRoot, realOutside
}

func TestSafeNewPathRejectsSymlinkEscape(t *testing.T) {
	root, _ := sandboxWithSymlink(t)

	// Every one of these used to pass: the missing middle segment made the
	// old code fall back to a plain string prefix check, which never saw the
	// symlink. MkdirAll would then follow it out of the sandbox.
	escapes := []string{
		"link/sub/pwned.txt",       // missing dir under a symlink
		"link/a/b/c/pwned.txt",     // several missing levels
		"link/sub/../pwned.txt",    // ".." inside the missing tail
		"../outside/pwned.txt",     // plain traversal
		"link/../../outside/x.txt", // traversal through the symlink
	}
	for _, p := range escapes {
		got, err := safeNewPath(root, p)
		if err == nil {
			t.Errorf("safeNewPath(%q) = %q, want an error", p, got)
		}
	}
}

func TestSafeNewPathAllowsLegitimateNewPaths(t *testing.T) {
	root, _ := sandboxWithSymlink(t)

	allowed := []string{
		"file.txt",
		"newdir/file.txt",
		"a/b/c/deep.txt",
		"./file.txt",
	}
	for _, p := range allowed {
		got, err := safeNewPath(root, p)
		if err != nil {
			t.Errorf("safeNewPath(%q) returned %v, want success", p, err)
			continue
		}
		if !withinRoot(root, got) {
			t.Errorf("safeNewPath(%q) = %q, which is outside %q", p, got, root)
		}
	}
}

func TestSafeNewPathAllowsExistingFileInRoot(t *testing.T) {
	root, _ := sandboxWithSymlink(t)
	existing := filepath.Join(root, "already.txt")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := safeNewPath(root, "already.txt")
	if err != nil {
		t.Fatalf("safeNewPath on an existing file: %v", err)
	}
	if got != existing {
		t.Errorf("got %q, want %q", got, existing)
	}
}

func TestSafePathRejectsSymlinkEscape(t *testing.T) {
	root, outside := sandboxWithSymlink(t)
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := safePath(root, "link/secret.txt"); err == nil {
		t.Errorf("safePath read through symlink: %q", got)
	}
}
