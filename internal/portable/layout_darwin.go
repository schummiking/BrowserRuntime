package portable

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrUnavailable = errors.New("BrowserRuntime application data is unavailable or not writable")

type Layout struct{ Root, Data, Engines, Profiles, Settings, WebView string }

func Current() (Layout, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Layout{}, ErrUnavailable
	}
	return FromDataRoot(filepath.Join(base, "BrowserRuntime"))
}

func FromDataRoot(root string) (Layout, error) {
	if !filepath.IsAbs(root) {
		return Layout{}, ErrUnavailable
	}
	root = filepath.Clean(root)
	return Layout{root, root, filepath.Join(root, "engines"), filepath.Join(root, "profiles"), filepath.Join(root, "settings"), filepath.Join(root, "settings", "webkit")}, nil
}

func (l Layout) Prepare() error {
	for _, p := range []string{l.Root, l.Engines, l.Profiles, l.Settings, l.WebView} {
		if checkAncestors(p) != nil || os.MkdirAll(p, 0700) != nil {
			return ErrUnavailable
		}
		f, err := os.CreateTemp(p, ".writable-")
		if err != nil {
			return ErrUnavailable
		}
		closeErr := f.Close()
		removeErr := os.Remove(f.Name())
		if closeErr != nil || removeErr != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func checkAncestors(path string) error {
	if !filepath.IsAbs(path) {
		return ErrUnavailable
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrUnavailable
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

// macOS framework bundles contain relative symlinks. Only links resolving
// inside this engine tree are allowed; ancestors must remain real directories.
func CheckTree(root string) error {
	if checkAncestors(root) != nil {
		return ErrUnavailable
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return ErrUnavailable
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(p)
			if err != nil {
				return ErrUnavailable
			}
			rel, err := filepath.Rel(root, target)
			if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
				return ErrUnavailable
			}
		}
		return nil
	})
}
