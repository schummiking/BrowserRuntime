package portable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMacDataLayoutAndRedirects(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := FromDataRoot(filepath.Join(base, "Application Support", "BrowserRuntime"))
	if err != nil || l.Prepare() != nil {
		t.Fatal("prepare app data")
	}
	if l.Profiles != filepath.Join(l.Root, "profiles") {
		t.Fatal("profile layout")
	}
	if err = os.Symlink(l.Root, filepath.Join(base, "redirect")); err != nil {
		t.Fatal(err)
	}
	r, _ := FromDataRoot(filepath.Join(base, "redirect", "child"))
	if r.Prepare() == nil {
		t.Fatal("accepted redirected data directory")
	}
	if err = os.WriteFile(filepath.Join(l.Engines, "resource"), []byte("resource"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("resource", filepath.Join(l.Engines, "link")); err != nil {
		t.Fatal(err)
	}
	if CheckTree(l.Engines) != nil {
		t.Fatal("rejected internal framework link")
	}
	if err = os.Symlink(base, filepath.Join(l.Engines, "escape")); err != nil {
		t.Fatal(err)
	}
	if CheckTree(l.Engines) == nil {
		t.Fatal("accepted escaping framework link")
	}
}
