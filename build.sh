#!/usr/bin/env bash
# Builds the Windows executables into dist/.
# Requires Go 1.22+ (rsrc is installed automatically).
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-0.1.0}"
# -mod=mod lets Go fill in missing go.sum entries on the first build.
export GOFLAGS="${GOFLAGS:--mod=mod}"
command -v rsrc >/dev/null || go install github.com/akavel/rsrc@latest
export PATH="$PATH:$(go env GOPATH)/bin"
go run ./tools/genres
rsrc -manifest build/app.manifest -ico build/app.ico -arch amd64 -o cmd/WifiPhotoReceiver/rsrc_windows_amd64.syso
rsrc -ico build/app.ico -arch amd64 -o cmd/wpr/rsrc_windows_amd64.syso
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$VERSION" -o dist/SPMAG-WifiPhotoReceiver.exe ./cmd/WifiPhotoReceiver
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/wpr.exe ./cmd/wpr
cp README.md LICENSE dist/
ls -la dist
