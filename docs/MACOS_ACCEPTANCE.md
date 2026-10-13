# macOS acceptance — 2026-10-12

Host: Apple Silicon Mac, Go 1.27.0, Wails 2.15.0.

## Automated checks

- `go test ./...`: passed, including archive traversal/tampering, executable
  permissions, file locks, real process enumeration, and Keychain round-trip tests.
- `go test -race ./internal/...`: passed.
- `go vet ./...`: passed.
- Frontend: all 44 tests passed; TypeScript/Vite production build passed.
- Windows packages and tests cross-compiled with
  `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -exec=true ./...`.
  This is compilation verification, not execution on Windows.
- Universal app contains `x86_64 arm64`; `codesign --verify --deep --strict`
  passed. DMG verification passed with `hdiutil verify`.

## Native UI verification

- Opened the real Wails/WebKit desktop window and switched English/简体中文.
- Downloaded the official Mac arm64 engine through the app, verifying SHA-256,
  extraction, framework links, and the installed manifest.
- Created an isolated `Mac 验收` profile and opened a native Chromium window.
- Confirmed the Running state and refusal to quit while a profile is active.
- Activated the existing browser by its exact process ID (confirmed active in
  macOS) without creating another process.
- Stopped the browser gracefully and observed Stopped before exiting.
- Installed the universal build at `/Applications/BrowserRuntime.app` and
  confirmed the profile persisted.
- Saved synthetic proxy credentials for `127.0.0.1:1`, quit and reopened the
  installed app, and confirmed the saved password was readable from Keychain.
  Restored the test profile to Direct after the check. No external proxy was used.

## Remaining limits

- Intel is build-verified; no Intel hardware run was performed.
- The app is ad-hoc signed, not Developer ID signed or notarized.
- The free macOS engine is Chromium 145; the upstream Windows engine is 146.
  This does not validate newer CloakBrowser Pro builds.
- External authenticated proxy connectivity was not tested with user credentials.
- Windows runtime behavior was not retested on a Windows host.
