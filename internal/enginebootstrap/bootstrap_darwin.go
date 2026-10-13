package enginebootstrap

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const Version = "145.0.7632.109.2"
const executableRelative = "Chromium.app/Contents/MacOS/Chromium"

var DownloadURL, ZipSHA256 = macRelease(runtime.GOARCH)
var required = []string{executableRelative, "Chromium.app/Contents/Info.plist", "Chromium.app/Contents/Frameworks/Chromium Framework.framework/Versions/145.0.7632.109/Chromium Framework"}

func macRelease(arch string) (string, string) {
	switch arch {
	case "arm64":
		return "https://github.com/CloakHQ/CloakBrowser/releases/download/chromium-v" + Version + "/cloakbrowser-darwin-arm64.tar.gz", "505582aa1bd3971c577f70e0cbbe016431702bdb693529abfd943b5bd9120c1c"
	case "amd64":
		return "https://github.com/CloakHQ/CloakBrowser/releases/download/chromium-v" + Version + "/cloakbrowser-darwin-x64.tar.gz", "a0a061b1945db55b3bbe95986a82b67803311c0fedabb3946e7ab0172273daac"
	default:
		return "", ""
	}
}

func engineHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	var value string
	if info.Mode()&os.ModeSymlink != 0 {
		value, err = os.Readlink(path)
		value = "symlink:" + value
	} else {
		value, err = hashFile(path)
		if info.Mode()&0111 != 0 {
			value = "executable:" + value
		}
	}
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:]), nil
}

func (m *Manager) extract(ctx context.Context, archive, staging string) error {
	f, err := os.Open(archive)
	if err != nil {
		return ErrDownload
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return ErrDownload
	}
	defer gz.Close()
	r := tar.NewReader(gz)
	seen := map[string]bool{}
	links := map[string]string{}
	files := map[string]string{}
	var total uint64
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil || ctx.Err() != nil || len(seen) >= 10000 {
			return ErrDownload
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !safeName(name) || (name != "Chromium.app" && !strings.HasPrefix(name, "Chromium.app/")) || seen[strings.ToLower(name)] || h.Size < 0 || uint64(h.Size) > maxExpanded-total {
			return ErrDownload
		}
		seen[strings.ToLower(name)] = true
		total += uint64(h.Size)
		path := filepath.Join(staging, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if os.MkdirAll(path, 0700) != nil {
				return ErrDownload
			}
		case tar.TypeSymlink:
			// Defer creation so no later archive entry can traverse a symlink.
			if !safeName(h.Linkname) {
				return ErrDownload
			}
			links[name] = h.Linkname
		case tar.TypeReg:
			if os.MkdirAll(filepath.Dir(path), 0700) != nil {
				return ErrDownload
			}
			mode := os.FileMode(0600)
			if h.Mode&0111 != 0 {
				mode = 0700
			}
			out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return ErrDownload
			}
			n, copyErr := copyContext(ctx, out, r)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil || n != h.Size {
				return ErrDownload
			}
			files[name] = ""
		default:
			return ErrDownload
		}
	}
	for name, target := range links {
		path := filepath.Join(staging, filepath.FromSlash(name))
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.Symlink(target, path) != nil {
			return ErrDownload
		}
		files[name] = ""
	}
	for name := range files {
		digest, err := engineHash(filepath.Join(staging, filepath.FromSlash(name)))
		if err != nil {
			return ErrDownload
		}
		files[name] = digest
	}
	for _, name := range required {
		if files[name] == "" {
			return ErrDownload
		}
	}
	data, err := json.Marshal(manifest{1, Version, m.config.digest, files})
	if err != nil || ctx.Err() != nil {
		return ErrDownload
	}
	return os.WriteFile(filepath.Join(staging, manifestName), data, 0600)
}
