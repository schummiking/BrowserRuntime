#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Go 1.27 requires macOS 13. Build both native slices with the installed SDK.
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=13.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=13.0"
go run github.com/wailsapp/wails/v2/cmd/wails@v2.15.0 build -platform darwin/universal
app="build/bin/BrowserRuntime.app"
codesign --verify --deep --strict "$app"
archive="build/bin/BrowserRuntime-v0.1.0-macOS-universal.zip"
ditto -c -k --sequesterRsrc --keepParent "$app" "$archive"

staging=$(mktemp -d "${TMPDIR:-/tmp}/browserruntime-dmg.XXXXXX")
trap 'rm -rf "$staging"' EXIT
ditto "$app" "$staging/BrowserRuntime.app"
ln -s /Applications "$staging/Applications"
cp docs/MACOS_README.md "$staging/README.md"
hdiutil create -volname BrowserRuntime -srcfolder "$staging" -ov -format UDZO build/bin/BrowserRuntime-v0.1.0-macOS-universal.dmg
shasum -a 256 "$archive" build/bin/BrowserRuntime-v0.1.0-macOS-universal.dmg > build/bin/SHA256SUMS-macOS.txt
