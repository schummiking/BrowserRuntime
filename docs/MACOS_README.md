# BrowserRuntime for macOS

Requires macOS 13 or later. The universal app includes Apple Silicon and Intel
executables. Apple Silicon is tested on real hardware; Intel is build-verified.

1. Open the DMG and drag BrowserRuntime into Applications, or unzip the ZIP.
2. Open BrowserRuntime. This build is ad-hoc signed, not Apple-notarized. If
   Gatekeeper requires approval, review the app in System Settings → Privacy &
   Security and use the normal macOS Open Anyway flow. Do not disable Gatekeeper.
3. Click **Download and install** to fetch the official CloakBrowser engine.
4. Create a profile and click **Launch**. **Open** activates its existing window;
   **Stop** asks the browser to quit gracefully. Stop running profiles before
   quitting the manager.

The English/简体中文 switch is at the top right. Profiles persist between launches.

## Data and credentials

Data lives in `~/Library/Application Support/BrowserRuntime/`, independently of
the app bundle. This includes engines, profiles, settings, and profile lock files.
Replacing the application does not remove profiles. Do not share or commit this
directory: browser profiles can contain logged-in sessions.

Proxy passwords are encrypted with AES-256-GCM using a random key stored in the
current user's macOS Keychain (`com.schummiking.browserruntime.proxy-key`, account `v1`).
Keychain access failure blocks credential operations; there is no plaintext
fallback. Windows DPAPI secrets cannot be imported into this Mac build.

## Engine version

macOS pins the official free **145.0.7632.109.2** release, with independent SHA-256
digests for arm64 and x64. The Windows build keeps **146.0.7680.177.5**. That Windows
release has no macOS asset. The engine is downloaded directly from CloakHQ and is
not bundled or redistributed in this app. Downloads are verified before extraction
and every engine file and framework link is checked before launch.

This older Mac engine does not include fixes in newer Chromium releases. This
port does not add CloakBrowser Pro licensing or claim parity with its latest engine.

## Build and verification

Install Go 1.27, a Node version supported by `frontend/package.json` dependencies
(Node 22.22.2+ or 24.15+), and Xcode Command Line Tools. Then run:

```sh
go test ./...
go test -race ./internal/...
go vet ./...
cd frontend && npm ci && npm test && npm run build && cd ..
bash scripts/build-macos.sh
```

Artifacts are in `build/bin/`. The build script produces a universal `.app`, ZIP,
DMG, and SHA-256 checksums. Distributing a notarized release requires an Apple
Developer ID certificate and notarization credentials supplied by the maintainer.

The fork retains the upstream Windows build script and platform-specific process,
DPAPI, and ZIP extraction implementations. Upstream did not include a source
license file in its initial snapshot; this fork does not assign one on its behalf.
