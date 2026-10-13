//go:build windows

package enginebootstrap

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const Version = "146.0.7680.177.5"
const DownloadURL = "https://github.com/CloakHQ/CloakBrowser/releases/download/chromium-v146.0.7680.177.5/cloakbrowser-windows-x64.zip"
const ZipSHA256 = "b213795cb32c3169f766c74ce1d0275fc89d3df256de39c04da7fb4c23b7fdbe"
const executableRelative = "chrome.exe"

var required = []string{"chrome.exe", "chrome.dll", "chrome_elf.dll", "resources.pak", "icudtl.dat", "locales/en-US.pak"}

func engineHash(path string) (string, error) { return hashFile(path) }

func (m *Manager) extract(ctx context.Context, zipPath, staging string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return ErrDownload
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > 10000 {
		return ErrDownload
	}
	// Official archive contains one top-level directory. Also accept a flat archive
	// for injected tests; never search recursively for an arbitrary executable.
	prefix := ""
	for _, f := range reader.File {
		if strings.HasSuffix(f.Name, "/chrome.exe") {
			prefix = strings.TrimSuffix(f.Name, "chrome.exe")
			break
		}
	}
	if prefix != "" && strings.Count(strings.TrimSuffix(prefix, "/"), "/") != 0 {
		return ErrDownload
	}
	seen := map[string]bool{}
	files := map[string]string{}
	var total uint64
	for _, f := range reader.File {
		if ctx.Err() != nil {
			return ErrDownload
		}
		if !safeName(f.Name) || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return ErrDownload
		}
		name := strings.TrimPrefix(f.Name, prefix)
		if name == "" && f.FileInfo().IsDir() {
			continue
		}
		if prefix != "" && !strings.HasPrefix(f.Name, prefix) {
			return ErrDownload
		}
		if !safeName(name) || strings.EqualFold(name, manifestName) {
			return ErrDownload
		}
		lower := strings.ToLower(strings.TrimSuffix(name, "/"))
		if seen[lower] {
			return ErrDownload
		}
		seen[lower] = true
		if f.UncompressedSize64 > 1<<30 || total > maxExpanded-f.UncompressedSize64 {
			return ErrDownload
		}
		total += f.UncompressedSize64
		dest := filepath.Join(staging, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			if os.MkdirAll(dest, 0700) != nil {
				return ErrDownload
			}
			continue
		}
		if os.MkdirAll(filepath.Dir(dest), 0700) != nil {
			return ErrDownload
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return ErrDownload
		}
		in, err := f.Open()
		if err != nil {
			out.Close()
			return ErrDownload
		}
		h := sha256.New()
		n, copyErr := copyContext(ctx, io.MultiWriter(out, h), io.LimitReader(in, int64(f.UncompressedSize64)+1))
		in.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || uint64(n) != f.UncompressedSize64 {
			return ErrDownload
		}
		files[name] = hex.EncodeToString(h.Sum(nil))
	}
	for _, name := range required {
		if files[name] == "" {
			return ErrDownload
		}
	}
	data, err := json.Marshal(manifest{1, Version, m.config.digest, files})
	if err != nil {
		return ErrDownload
	}
	if ctx.Err() != nil {
		return ErrDownload
	}
	return os.WriteFile(filepath.Join(staging, manifestName), data, 0600)
}
