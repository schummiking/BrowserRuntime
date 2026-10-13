package enginebootstrap

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func archiveFixture(t *testing.T, extra *tar.Header) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range required {
		if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 4, Mode: 0755}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte("test")); err != nil {
			t.Fatal(err)
		}
	}
	if extra != nil {
		if err = tw.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMacEngineIntegrityAndPermissions(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(root)
	if err = m.extract(context.Background(), archiveFixture(t, nil), root); err != nil {
		t.Fatal(err)
	}
	if err = m.Verify(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(m.Executable(), 0600); err != nil {
		t.Fatal(err)
	}
	if m.Verify() == nil {
		t.Fatal("accepted stripped executable permission")
	}
	if err = os.Chmod(m.Executable(), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(m.Executable(), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if m.Verify() == nil {
		t.Fatal("accepted tampered engine")
	}
}

func TestMacArchiveRejectsTraversalAndSpecialFiles(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "Chromium.app/escape", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
		{Name: "Chromium.app/fifo", Typeflag: tar.TypeFifo},
		{Name: executableRelative, Typeflag: tar.TypeReg},
	} {
		t.Run(h.Name, func(t *testing.T) {
			root := t.TempDir()
			m := New(root)
			if m.extract(context.Background(), archiveFixture(t, h), root) == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}
